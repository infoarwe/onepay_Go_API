package models

import (
	"context"
	"strconv"
	"strings"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This file ports driver_location_history_update_new.js (the handler the
// legacy Node service actually dispatches to for tenants-
// see mobileapi_http.js's `!I_O(CUSTOMIZATION_FOLDERS, req.uniqueSocket)`
// branch) and its supporting common/database_new.js query functions.
//
// Scope: the driver-device/location/trip-dispatch flow is ported in full.
// The in-progress-trip fare recompute (night/evening surcharge, waiting
// cost, rental/outstation formula) is deliberately NOT ported here - it
// needs several more collections and can't be verified against real data
// from this environment. See driverLocationController.go for where that
// gap is surfaced in the response.

// Device-check status codes, mirroring database_new.js's check_driver_device
// resolve() values.
const (
	DeviceStatusNotFound        = 0
	DeviceStatusOK              = 1
	DeviceStatusAlreadyLoggedIn = 2
	DeviceStatusLoggedOut       = 3
)

// ---- tolerant local converters -------------------------------------------
// Package-local rather than helpers/, to avoid an import cycle: helpers/
// (domainHelper.go) imports database/, and database/ imports models/.

func toInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int32:
		return int64(val)
	case int:
		return int64(val)
	case float64:
		return int64(val)
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	default:
		return 0
	}
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case int:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

// bsonSub safely reads a nested sub-document off a bson.M, tolerating a
// missing key or unexpected type by returning an empty doc instead of nil,
// so callers can chain field reads without repeated nil checks.
func bsonSub(doc bson.M, key string) bson.M {
	if doc == nil {
		return bson.M{}
	}
	switch sub := doc[key].(type) {
	case bson.M:
		return sub
	case bson.D:
		return sub.Map()
	default:
		return bson.M{}
	}
}

func cronTimeToSeconds(cronTime string) int {
	if cronTime == "" {
		cronTime = "00:00"
	}
	parts := strings.Split(cronTime, ":")
	if len(parts) < 2 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h*3600 + m*60
}

// CheckDriverDevice ports database_new.js's check_driver_device(): a single
// aggregation against MDB_PEOPLE that resolves the driver's active
// taxi_mapping, current trip (passengers_logs), rider (passengers), taxi +
// model, and the trip's company - i.e. everything the rest of the handler
// needs, joined once instead of once per field access (mirrors
// socket.ProfileInfo in the Node version).
//
// The active-mapping date window is skipped for drivers already 'B'/'A'
// (busy/on a trip), same guard as the legacy query, so a mapping expiring
// mid-trip doesn't kick a driver off it.
//
// Returns the legacy resolve() status:
//
//	1 = ok, profile populated (driverinfo.status is set and != 'F', OR
//	    status is F/absent but device_id on file matches deviceToken)
//	2 = driver found but device_id mismatch (session active elsewhere)
//	3 = driver found, status F/absent, and no device_id on file at all
//	0 = no matching people/mapping row (e.g. no currently-active taxi_mapping)
func CheckDriverDevice(db *mongo.Database, driverID int64, deviceToken, driverStatus, cronTime string) (int, bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	now := time.Now().UTC()
	seconds := cronTimeToSeconds(cronTime)

	mappingMatch := bson.M{"taxi_mapping.mapping_status": "A"}
	if driverStatus != "B" && driverStatus != "A" {
		mappingMatch["taxi_mapping.mapping_startdate"] = bson.M{"$lte": now}
		mappingMatch["taxi_mapping.mapping_enddate"] = bson.M{"$gte": now}
	}

	scheduleTripsPipeline := bson.A{
		bson.M{"$match": bson.M{
			"dispatch_status":  bson.M{"$eq": 0},
			"rejected_drivers": bson.M{"$nin": bson.A{driverID}},
		}},
		bson.M{"$match": bson.M{"$or": bson.A{
			bson.M{"driver_id": 0},
			bson.M{"driver_id": driverID},
		}}},
		bson.M{"$project": bson.M{
			"plogs_id":     1,
			"_id":          0,
			"location":     1,
			"taxi_modelid": 1,
			"updatetime_difference": bson.M{
				"$multiply": bson.A{
					bson.M{"$subtract": bson.A{now, "$schedule_time"}},
					0.001,
				},
			},
		}},
		bson.M{"$match": bson.M{"$and": bson.A{
			bson.M{"updatetime_difference": bson.M{"$gte": float64(-seconds)}},
			bson.M{"updatetime_difference": bson.M{"$lte": 0.0}},
		}}},
		bson.M{"$limit": 1},
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"_id": driverID, "user_type": "D"}}},
		{{Key: "$unwind", Value: bson.M{"path": "$taxi_mapping", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$match", Value: mappingMatch}},
		{{Key: "$limit", Value: int64(1)}},
		{{Key: "$lookup", Value: bson.M{
			"from":     collections.SCHEDULE_TRIPS,
			"pipeline": scheduleTripsPipeline,
			"as":       "schedule_trips",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$schedule_trips", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.PASSENGERS_LOGS,
			"localField":   "current_trip.trip_id",
			"foreignField": "_id",
			"as":           "plogs",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$plogs", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.PASSENGERS,
			"localField":   "plogs.passengers_id",
			"foreignField": "_id",
			"as":           "passengers",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$passengers", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.TAXI,
			"localField":   "taxi_mapping.mapping_taxiid",
			"foreignField": "_id",
			"as":           "taxi",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$taxi", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.MOTOR_MODEL,
			"localField":   "taxi.taxi_model",
			"foreignField": "_id",
			"as":           "motor_model",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$motor_model", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from":         collections.COMPANY,
			"localField":   "plogs.company_id",
			"foreignField": "_id",
			"as":           "company",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$company", "preserveNullAndEmptyArrays": true}}},
	}

	cursor, err := db.Collection(collections.PEOPLE).Aggregate(ctx, pipeline)
	if err != nil {
		return DeviceStatusNotFound, bson.M{}, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return DeviceStatusNotFound, bson.M{}, err
	}
	if len(results) == 0 {
		return DeviceStatusNotFound, bson.M{}, nil
	}
	profile := results[0]

	driverInfo := bsonSub(profile, "driverinfo")
	if status, ok := driverInfo["status"]; ok && status != nil && toString(status) != "F" {
		return DeviceStatusOK, profile, nil
	}
	if deviceID, ok := profile["device_id"]; ok && deviceID != nil && toString(deviceID) != "" {
		if toString(deviceID) == deviceToken {
			return DeviceStatusOK, profile, nil
		}
		return DeviceStatusAlreadyLoggedIn, bson.M{}, nil
	}
	return DeviceStatusLoggedOut, bson.M{}, nil
}

// MappingExpiry ports database_new.js's mapping_expiry(): finds the
// driver's latest currently-active taxi_driver_mapping row and stamps it
// onto people.taxi_mapping, so a follow-up CheckDriverDevice retry picks it
// up. Best-effort like the legacy version - finding nothing isn't an error,
// it just means the retry resolves status 0 again.
func MappingExpiry(db *mongo.Database, driverID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	match := bson.M{
		"mapping_status":    "A",
		"mapping_driverid":  driverID,
		"mapping_startdate": bson.M{"$lte": now},
		"mapping_enddate":   bson.M{"$gte": now},
	}

	var mapping bson.M
	err := db.Collection(collections.TAXI_DRIVER_MAPPING).
		FindOne(ctx, match, options.FindOne().SetSort(bson.D{{Key: "mapping_enddate", Value: 1}})).
		Decode(&mapping)
	if err == mongo.ErrNoDocuments {
		return nil
	}
	if err != nil {
		return err
	}

	_, err = db.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{"taxi_mapping": mapping}},
	)
	return err
}

// UpdateDriverLocationParams mirrors the $update_driver_array the legacy
// handler builds before calling update_driver_location.
type UpdateDriverLocationParams struct {
	DriverID      int64
	Latitude      string
	Longitude     string
	Status        string
	UpdateDate    time.Time
	PhoneModel    string
	Bearings      float64
	Accuracy      float64
	Brand         string
	Model         string
	ServiceStatus bool
	VersionCode   int
	CarrierName   string
}

// UpdateDriverLocation ports database_new.js's update_driver_location():
// upserts the driver's current position/status/device-diagnostics onto
// their MDB_PEOPLE doc. Skips writing `loc` when status is busy/active and
// both coordinates are exactly (0,0) - same guard as the legacy version, so
// a zeroed-out ping mid-trip doesn't clobber the driver's last known
// position.
func UpdateDriverLocation(db *mongo.Database, p UpdateDriverLocationParams) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	set := bson.M{}
	if p.Status != "" {
		set["driverinfo.status"] = p.Status
	}

	lat := toFloat64(p.Latitude)
	lng := toFloat64(p.Longitude)
	isZero := lat == 0 && lng == 0
	if p.Longitude != "" && !((p.Status == "B" || p.Status == "A") && isZero) {
		set["loc"] = bson.M{"type": "Point", "coordinates": bson.A{lng, lat}}
	}

	if p.PhoneModel != "" {
		set["driverinfo.phone_model"] = p.PhoneModel
	}
	set["driverinfo.bearings"] = p.Bearings
	set["driverinfo.accuracy"] = p.Accuracy
	set["driverinfo.brand"] = p.Brand
	set["driverinfo.model"] = p.Model
	set["driverinfo.service_status"] = p.ServiceStatus
	set["driverinfo.version_code"] = p.VersionCode
	set["driverinfo.carrier_name"] = p.CarrierName
	if !p.UpdateDate.IsZero() {
		set["driverinfo.update_date"] = p.UpdateDate
	}

	_, err := db.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": p.DriverID},
		bson.M{"$set": set},
		options.Update().SetUpsert(true),
	)
	return err
}

// ParseLastLocation ports collect_location_lat_lng_from_request(): the
// client sends a '|'-delimited trail of "lat,lng" fixes (confirmed against
// a real payload: "11.401121,76.858741|" for a Coimbatore-area driver -
// 11.40 is the latitude); only the last (most recent, non-empty-tail) fix
// is used for the driver's current position.
func ParseLastLocation(locations string) (latitude, longitude string) {
	parts := strings.Split(locations, "|")
	if len(parts) > 1 {
		parts = parts[:len(parts)-1] // drop the trailing empty element from a trailing '|'
	}
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "0.0", "0.0"
	}
	pair := strings.Split(parts[len(parts)-1], ",")
	lat, lng := "0.0", "0.0"
	if len(pair) > 0 && pair[0] != "" {
		lat = pair[0]
	}
	if len(pair) > 1 && pair[1] != "" {
		lng = pair[1]
	}
	return lat, lng
}

// parseLocationTrail turns the client's '|'-delimited "lat,lng" trail into
// GeoJSON-ordered [lng, lat] points for a MultiPoint, dropping empty
// elements (a trailing '|', or a blank fix).
func parseLocationTrail(locations string) bson.A {
	out := bson.A{}
	for _, part := range strings.Split(locations, "|") {
		if part == "" {
			continue
		}
		pair := strings.Split(part, ",")
		if len(pair) < 2 {
			continue
		}
		out = append(out, bson.A{toFloat64(pair[1]), toFloat64(pair[0])})
	}
	return out
}

// SaveDriverLocationHistoryResult mirrors the legacy resolve() shapes: 2/5
// are the driver_id/trip_id-missing short circuits, 1 is the normal append
// (Distance carries the client-reported trip distance once trip_loc exists).
type SaveDriverLocationHistoryResult struct {
	Status   int
	Distance string
}

// SaveDriverLocationHistory ports database_new.js's
// save_driver_location_history(): appends the trip's GeoJSON MultiPoint
// trail onto the driver's MDB_PEOPLE doc (matched by current_trip.trip_id),
// initializing it on the trip's first reported point.
//
// Simplified vs. the legacy version: points are appended (and
// driverinfo.distance stamped) whenever a trip is in progress here, rather
// than gated behind the specific numeric travel_status the legacy schema
// uses to distinguish "en route to pickup" vs "en route to drop" - that
// distinction only affects whether the append happens, not the response
// shape, and isn't verifiable against real data from this environment.
func SaveDriverLocationHistory(db *mongo.Database, profile bson.M, driverID, tripID int64, locationsRaw, distance string) (SaveDriverLocationHistoryResult, error) {
	if driverID == 0 {
		return SaveDriverLocationHistoryResult{Status: 2, Distance: "0"}, nil
	}
	if tripID == 0 {
		return SaveDriverLocationHistoryResult{Status: 5, Distance: "0"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	coords := parseLocationTrail(locationsRaw)
	match := bson.M{"current_trip.trip_id": tripID}

	if len(bsonSub(profile, "trip_loc")) == 0 {
		_, err := db.Collection(collections.PEOPLE).UpdateOne(ctx, match, bson.M{
			"$set": bson.M{"trip_loc": bson.M{"type": "MultiPoint", "coordinates": coords}},
		})
		if err != nil {
			return SaveDriverLocationHistoryResult{}, err
		}
		return SaveDriverLocationHistoryResult{Status: 1, Distance: "0"}, nil
	}

	_, err := db.Collection(collections.PEOPLE).UpdateOne(ctx, match, bson.M{
		"$push": bson.M{"trip_loc.coordinates": bson.M{"$each": coords}},
		"$set":  bson.M{"driverinfo.distance": toFloat64(distance)},
	})
	if err != nil {
		return SaveDriverLocationHistoryResult{}, err
	}
	if distance == "" {
		distance = "0"
	}
	return SaveDriverLocationHistoryResult{Status: 1, Distance: distance}, nil
}

// TripCancelInfo is what trip_id_valid_then's direct passengers_logs lookup
// needs to pick the right cancellation message.
type TripCancelInfo struct {
	Found        bool
	TravelStatus int
	PaymentType  int
}

// FindTripCancelInfo queries passengers_logs directly by the request's
// trip_id (not via the joined ProfileInfo.plogs, which is joined off the
// driver's *own* current_trip and may be a different, stale trip precisely
// when this lookup is needed) - same as trip_id_valid_then's inline query.
func FindTripCancelInfo(db *mongo.Database, tripID int64) (TripCancelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := db.Collection(collections.PASSENGERS_LOGS).FindOne(
		ctx,
		bson.M{"_id": tripID},
		options.FindOne().SetProjection(bson.M{"travel_status": 1, "transaction_info": 1}),
	).Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return TripCancelInfo{}, nil
	}
	if err != nil {
		return TripCancelInfo{}, err
	}

	info := TripCancelInfo{Found: true, TravelStatus: int(toInt64(raw["travel_status"]))}
	if txn := bsonSub(raw, "transaction_info"); len(txn) > 0 {
		info.PaymentType = int(toInt64(txn["payment_type"]))
	}
	return info, nil
}

// BuildAvailableTripDetails ports the trip_details payload
// driver_status_free_then_take_trips_assign_to_driver() builds when a free
// driver has a newly-dispatched trip waiting. Sources are the ProfileInfo
// sub-documents CheckDriverDevice already joined - plogs (passengers_logs)
// for most trip fields, passengers for the rider's name/phone, motor_model
// for the vehicle model name - field names taken from
// get_passenger_log_detail()'s straight-copy field list. Fields sourced
// from company_info in the legacy code (cancellation_nfree,
// fare_calculation_type, brand_type, default_unit) are omitted here since
// that source doc is always `{}` there too (a latent bug confirmed via
// research - company_info is never populated by the aggregation that builds
// it), so they'd be blank either way.
//
// Returns (details, ok) - ok is false when plogs doesn't actually belong to
// haveAvailableTripID (stale/mismatched profile), matching
// get_passenger_log_detail()'s "resolve({})" fallback.
func BuildAvailableTripDetails(profile bson.M, haveAvailableTripID int64, notificationSeconds int) (bson.M, bool) {
	if haveAvailableTripID == 0 {
		return nil, false
	}
	plogs := bsonSub(profile, "plogs")
	if toInt64(plogs["_id"]) != haveAvailableTripID {
		return nil, false
	}
	passengers := bsonSub(profile, "passengers")
	motorModel := bsonSub(profile, "motor_model")

	str := func(m bson.M, key string) string { return toString(m[key]) }
	orZero := func(v interface{}) interface{} {
		if v == nil {
			return 0
		}
		return v
	}

	modelName := str(motorModel, "model_name")
	modelNameArabic := str(motorModel, "model_name_arabic")
	if modelNameArabic == "" {
		modelNameArabic = modelName
	}
	passengerName := str(passengers, "passenger_name")
	if passengerName == "" {
		passengerName = str(plogs, "passenger_name")
	}
	passengerPhone := str(passengers, "phone")
	if passengerPhone == "" {
		passengerPhone = str(plogs, "passenger_phone")
	}

	if notificationSeconds <= 0 {
		notificationSeconds = 15
	}
	notifyMinutes := notificationSeconds / 60
	notifySeconds := notificationSeconds % 60
	minutesStr := strconv.Itoa(notifyMinutes)
	if notifyMinutes < 10 {
		minutesStr = "0" + minutesStr
	}
	secondsStr := strconv.Itoa(notifySeconds)
	if notifySeconds < 10 {
		secondsStr = "0" + secondsStr
	}

	bookingDetails := bson.M{
		"is_corporate_booking":     orZero(plogs["is_corporate_booking"]),
		"is_on_my_way_trip":        orZero(plogs["is_on_my_way_trip"]),
		"corporate_company_id":     orZero(plogs["corporate_company_id"]),
		"corporate_pay":            orZero(plogs["corporate_pay"]),
		"babyseater_name1":         str(plogs, "babyseater_name1"),
		"baby_seatercount1":        orZero(plogs["baby_seatercount1"]),
		"babyseater_name2":         str(plogs, "babyseater_name2"),
		"baby_seatercount2":        orZero(plogs["baby_seatercount2"]),
		"babyseater_name3":         str(plogs, "babyseater_name3"),
		"baby_seatercount3":        orZero(plogs["baby_seatercount3"]),
		"instruction":              str(plogs, "instruction"),
		"airport_code":             str(plogs, "airport_code"),
		"airport_name":             str(plogs, "airport_name"),
		"flight_number":            str(plogs, "flight_number"),
		"pickupplace":              str(plogs, "current_location"),
		"dropplace":                str(plogs, "drop_location"),
		"pickup_time":              plogs["pickup_time"],
		"driver_id":                profile["_id"],
		"passenger_id":             plogs["passengers_id"],
		"roundtrip":                "",
		"passenger_phone":          passengerPhone,
		"cityname":                 "",
		"distance_away":            "",
		"sub_logid":                plogs["sub_logid"],
		"drop_latitude":            plogs["drop_latitude"],
		"drop_longitude":           plogs["drop_longitude"],
		"taxi_id":                  plogs["taxi_id"],
		"taxi_modelid":             plogs["taxi_modelid"],
		"model_name":               modelName,
		"company_id":               plogs["company_id"],
		"model_name_arabic":        modelNameArabic,
		"pickup_latitude":          plogs["pickup_latitude"],
		"pickup_longitude":         plogs["pickup_longitude"],
		"bookedby":                 plogs["bookby"],
		"passenger_name":           passengerName,
		"profile_image":            "",
		"drop":                     str(plogs, "drop_location"),
		"passenger_payment_option": orZero(plogs["passenger_payment_option"]),
		"now_after":                plogs["now_after"],
	}

	return bson.M{
		"message":              "Booking request sent. You will receive driver confirmation shortly.",
		"status":               "1",
		"passengers_log_id":    haveAvailableTripID,
		"booking_details":      bookingDetails,
		"estimated_time":       plogs["time_to_reach_passen"],
		"notification_time":    notificationSeconds,
		"notification_minutes": minutesStr,
		"notification_seconds": secondsStr,
		"notes":                str(plogs, "notes_driver"),
		"pickup_notes":         str(plogs, "pickup_notes"),
		"dropoff_notes":        str(plogs, "dropoff_notes"),
		"belowspeed_mins":      orZero(plogs["belowspeed_mins"]),
		"trip_type":            tripType(plogs),
		"approx_fare":          orZero(plogs["approx_fare"]),
		"approx_distance":      orZero(plogs["approx_distance"]),
		"driver_noshow_time":   plogs["driver_noshow_time"],
	}, true
}

// tripType ports the trip_type derivation
// (driver_location_history_update_new.js:630-641): rental_outstation
// (1/2/3) maps to a distinct trip_type, overridden by 22 for corporate
// bookings.
func tripType(plogs bson.M) string {
	switch toString(plogs["rental_outstation"]) {
	case "1":
		return "2"
	case "2":
		return "3"
	case "3":
		return "4"
	}
	corporateCompanyID := toString(plogs["corporate_company_id"])
	if toString(plogs["is_corporate_booking"]) == "1" || (corporateCompanyID != "" && corporateCompanyID != "0") {
		return "22"
	}
	return "0"
}
