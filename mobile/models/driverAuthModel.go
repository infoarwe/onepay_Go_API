package models

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"time"

	"fmt"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This file ports moddriverapi201.php's `driver_login` case (BlueTaxi
// tenant, action_index() dispatcher) and its model dependencies in
// moddriverapi113.php/modmobileapi111extended.php.
//
// Scope/fidelity notes (see controllers/driverAuthController.go for the
// full flow and response shape):
//   - The legacy code runs three separate queries to distinguish
//     "phone not found" / "signup incomplete" / "wrong password" as
//     different error messages. AuthenticateDriver collapses these into one
//     findOne-by-phone (no password in the filter) and lets the caller
//     compare the returned `password` field itself - same three distinct
//     error paths, one round trip instead of three.
//   - driver_statistics (total_trip, cancelled_trips, today_earnings,
//     time_driven) is reimplemented with CORRECT semantics rather than
//     replicating three confirmed bugs found in the legacy PHP: (1)
//     get_driver_log_details() only ever reads its first Mongo result
//     despite the controller looping over it, so completed-trip counting
//     silently caps at 0 or 1; (2) get_driver_cancelled_trips() accepts a
//     driver_id parameter but never applies it to the query filter, so it
//     counts the whole tenant's cancellations for the day, not this
//     driver's; (3) get_time_driven() computes an hours component but never
//     includes it in the returned string, so a multi-hour shift renders as
//     e.g. "75:30" instead of "1:15:30". Reproducing known bugs in a fresh
//     implementation isn't "fidelity", it's just carrying bugs forward on
//     purpose - flagged here rather than silently diverging.
//   - FindDriverProfile's driver_referral_list lookup uses field names that
//     aren't confirmed against live data from this environment (the PHP
//     research covered the driver_login case's control flow in detail but
//     not every collection's exact schema) - best-effort, documented inline.
//     FindActiveShift/CloseActiveShift's driver_shift_history schema was
//     initially a similar guess but has since been confirmed against the
//     driver_recent_trip_list research (see driverTripListModel.go) and
//     corrected to match: shift_end (not a status field) marks open/closed.

// AuthenticateDriver looks up a driver by phone (+ tenant company_id, if
// resolved) and returns the raw record for the caller to validate against
// (password match, signup_status, trip_reject_block, status) - see the
// file doc comment for why this is one query instead of the legacy's three.
// Returns (nil, nil) when no such phone/company_id + user_type='D' row
// exists at all.
func AuthenticateDriver(tenantDB *mongo.Database, phone string, companyID int64) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	match := bson.M{"phone": phone, "user_type": "D"}
	if companyID != 0 {
		match["company_id"] = companyID
	}

	projection := bson.M{
		"status": 1, "login_status": 1, "login_from": 1, "device_token": 1,
		"device_id": 1, "company_id": 1, "driver_first_login": 1, "salutation": 1,
		"current_trip": 1, "trip_reject_block": 1, "password": 1, "signup_status": 1,
	}

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, match, options.FindOne().SetProjection(projection)).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// FindDriverProfile ports driver_profile() (modmobileapi111extended.php:1558-1714):
// an aggregation joining the driver's own MDB_PEOPLE doc with their company
// and referral record, gated by the same "currently active taxi_mapping"
// window CheckDriverDevice uses (see driverLocationModel.go) - taxi_mapping
// itself is an embedded field on the people doc, not a separate joined
// collection, per the legacy pipeline.
func FindDriverProfile(tenantDB *mongo.Database, driverID int64) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	now := time.Now().UTC()
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"_id": driverID, "user_type": "D"}}},
		{{Key: "$unwind", Value: bson.M{"path": "$driverinfo", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from": collections.COMPANY, "localField": "company_id", "foreignField": "_id", "as": "company",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$company", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$lookup", Value: bson.M{
			"from": collections.DRIVER_REF, "localField": "_id", "foreignField": "registered_driver_id", "as": "driver_ref",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$driver_ref", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$match", Value: bson.M{
			"_id": driverID, "user_type": "D",
			"taxi_mapping.mapping_status":    "A",
			"taxi_mapping.mapping_startdate": bson.M{"$lte": now},
			"taxi_mapping.mapping_enddate":   bson.M{"$gte": now},
		}}},
		{{Key: "$limit", Value: int64(1)}},
	}

	cursor, err := tenantDB.Collection(collections.PEOPLE).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// UpdateDriverPhone ports update_driver_phone(): persists the device
// registration + login_status onto the driver's MDB_PEOPLE doc.
func UpdateDriverPhone(tenantDB *mongo.Database, driverID int64, deviceID, deviceToken, deviceType, loginStatus string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := tenantDB.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{
			"device_id": deviceID, "device_token": deviceToken,
			"device_type": deviceType, "login_status": loginStatus,
		}},
	)
	return err
}

// ChangeDriverPassword ports change_password() (moddriverapi113.php:3546-3553):
// MD5-hashes the plaintext password and $sets it onto the driver's
// MDB_PEOPLE doc. MD5-with-no-salt is what driver_login's raw
// string-equality password check requires to keep working - reproduced for
// storage-format compatibility, not as a new design choice (see
// requests.DriverChangePasswordRequest's doc comment).
func ChangeDriverPassword(tenantDB *mongo.Database, driverID int64, plaintextPassword string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sum := md5.Sum([]byte(plaintextPassword))
	hashed := hex.EncodeToString(sum[:])

	_, err := tenantDB.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{"password": hashed}},
	)
	return err
}

// FindTaxiForDriver ports getTaxiforDriver(): the taxi currently mapped to
// this driver, via the same active-window match MappingExpiry uses (see
// driverLocationModel.go) against MDB_TAXI_DRIVER_MAPPING.
func FindTaxiForDriver(tenantDB *mongo.Database, driverID int64) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	match := bson.M{
		"mapping_driverid": driverID, "mapping_status": "A",
		"mapping_startdate": bson.M{"$lte": now},
		"mapping_enddate":   bson.M{"$gte": now},
	}

	var mapping bson.M
	err := tenantDB.Collection(collections.TAXI_DRIVER_MAPPING).
		FindOne(ctx, match, options.FindOne().SetProjection(bson.M{"mapping_taxiid": 1})).
		Decode(&mapping)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mapping["mapping_taxiid"], nil
}

// FindActiveShift ports get_active_shift(): the driver's currently open
// shift, if any. Schema confirmed via the driver_recent_trip_list research
// (fleeteracommonmodel.php's shift subsystem, ~lines 10775-11480): a
// driver_shift_history doc is {_id, driver_id, taxi_id, shift_start,
// shift_end (null while open), reason, closed_by, createdate} - "active"
// means shift_end is null, there's no separate status field. This corrects
// an earlier guess (a `shift_status` field) made before that subsystem had
// been traced.
func FindActiveShift(tenantDB *mongo.Database, driverID int64) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var shift bson.M
	err := tenantDB.Collection(collections.SHIFT_HISTORY).
		FindOne(
			ctx,
			bson.M{"driver_id": driverID, "shift_end": nil},
			options.FindOne().SetSort(bson.D{{Key: "shift_start", Value: -1}}),
		).
		Decode(&shift)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return shift, nil
}

// CloseActiveShift ports shift_out_common($driverID, 'manual')/
// process_shift_out(): closes the given open shift by stamping shift_end.
func CloseActiveShift(tenantDB *mongo.Database, shiftID interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := tenantDB.Collection(collections.SHIFT_HISTORY).UpdateOne(
		ctx,
		bson.M{"_id": shiftID},
		bson.M{"$set": bson.M{"shift_end": time.Now().UTC(), "closed_by": "manual"}},
	)
	return err
}

// DriverStatistics is the corrected (see file doc comment) equivalent of
// the legacy driver_statistics block.
type DriverStatistics struct {
	CompletedTrips  int64
	CancelledTrips  int64
	RejectedTrips   int64
	TotalEarnings   float64
	TodayEarnings   float64
	TimeDrivenToday string
}

// dayBoundsUTC returns [start, end) for "today" in the given timezone,
// expressed as UTC instants (Mongo stores/compares dates in UTC).
func dayBoundsUTC(loc *time.Location) (time.Time, time.Time) {
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}

// ComputeDriverStatistics gathers the driver's trip/earnings stats for the
// login response. loc is the tenant's timezone (see FindSiteInfo/timezone
// resolution in driverController.go's GetCoreConfig), used for "today"'s
// day boundaries.
func ComputeDriverStatistics(tenantDB *mongo.Database, driverID int64, loc *time.Location) (DriverStatistics, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dayStart, dayEnd := dayBoundsUTC(loc)
	logs := tenantDB.Collection(collections.PASSENGERS_LOGS)

	var stats DriverStatistics

	completed, err := logs.CountDocuments(ctx, bson.M{"driver_id": driverID, "travel_status": 1})
	if err != nil {
		return stats, err
	}
	stats.CompletedTrips = completed

	cancelled, err := logs.CountDocuments(ctx, bson.M{
		"driver_id": driverID, "travel_status": 9, "driver_reply": "C",
		"createdate": bson.M{"$gte": dayStart, "$lt": dayEnd},
	})
	if err != nil {
		return stats, err
	}
	stats.CancelledTrips = cancelled

	rejected, err := tenantDB.Collection(collections.REJECTION_HISTORY).CountDocuments(ctx, bson.M{
		"driver_id": driverID, "createdate": bson.M{"$gte": dayStart, "$lt": dayEnd},
	})
	if err != nil {
		return stats, err
	}
	stats.RejectedTrips = rejected

	stats.TotalEarnings, err = sumTransactionFare(ctx, logs, bson.M{"driver_id": driverID, "travel_status": 1})
	if err != nil {
		return stats, err
	}

	stats.TodayEarnings, err = sumTransactionFare(ctx, logs, bson.M{
		"driver_id": driverID, "travel_status": 1,
		"pickup_time": bson.M{"$gte": dayStart, "$lt": dayEnd},
	})
	if err != nil {
		return stats, err
	}

	stats.TimeDrivenToday, err = timeDrivenToday(ctx, logs, driverID, dayStart, dayEnd)
	if err != nil {
		return stats, err
	}

	return stats, nil
}

// sumTransactionFare mirrors get_driver_total_earnings()/the "today
// earnings" half of the legacy stats block: sum of
// transaction_info.fare across matching passengers_logs docs.
func sumTransactionFare(ctx context.Context, logs *mongo.Collection, match bson.M) (float64, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$unwind", Value: "$transaction_info"}},
		{{Key: "$group", Value: bson.M{"_id": nil, "total": bson.M{"$sum": "$transaction_info.fare"}}}},
	}
	cursor, err := logs.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, nil
	}
	return toFloat64(results[0]["total"]), nil
}

// timeDrivenToday mirrors get_time_driven(): total time between
// actual_pickup_time and drop_time across today's completed trips,
// formatted as "H:MM:SS" - unlike the legacy version, hours are actually
// included (see file doc comment).
func timeDrivenToday(ctx context.Context, logs *mongo.Collection, driverID int64, dayStart, dayEnd time.Time) (string, error) {
	match := bson.M{
		"driver_id": driverID, "msg_status": "R", "driver_reply": "A", "travel_status": 1,
		"createdate": bson.M{"$gte": dayStart, "$lt": dayEnd},
	}
	cursor, err := logs.Find(ctx, match, options.Find().SetProjection(bson.M{"actual_pickup_time": 1, "drop_time": 1}))
	if err != nil {
		return "0:00:00", err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return "0:00:00", err
	}

	var total time.Duration
	for _, r := range results {
		pickup, okP := r["actual_pickup_time"].(primitive.DateTime)
		drop, okD := r["drop_time"].(primitive.DateTime)
		if !okP || !okD {
			continue
		}
		diff := drop.Time().Sub(pickup.Time())
		if diff < 0 {
			diff = -diff
		}
		total += diff
	}

	hours := int64(total.Hours())
	minutes := int64(total.Minutes()) % 60
	seconds := int64(total.Seconds()) % 60
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds), nil
}

// FindRecentTripList ports get_recent_driver_trip_list(): the driver's 3
// most recent trips with a completed/pending-settlement travel_status and
// a recorded drop_time, joined with their taxi model.
func FindRecentTripList(tenantDB *mongo.Database, driverID int64) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"driver_id": driverID, "travel_status": bson.M{"$in": bson.A{9, 1}},
			"drop_time": bson.M{"$ne": nil},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "drop_time", Value: -1}}}},
		{{Key: "$limit", Value: int64(3)}},
		{{Key: "$lookup", Value: bson.M{
			"from": collections.MOTOR_MODEL, "localField": "taxi_modelid", "foreignField": "_id", "as": "motor_model",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$motor_model", "preserveNullAndEmptyArrays": true}}},
	}

	cursor, err := tenantDB.Collection(collections.PASSENGERS_LOGS).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	results := []bson.M{}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}

// FindEmergencyContacts ports get_emergency_contact($driverID, 'D')'s
// result shape: the driver's saved SOS contact list.
func FindEmergencyContacts(tenantDB *mongo.Database, driverID int64) ([]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID}, options.FindOne().SetProjection(bson.M{"emergency_contact": 1})).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return []interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	if contacts, ok := raw["emergency_contact"].(bson.A); ok {
		return contacts, nil
	}
	return []interface{}{}, nil
}
