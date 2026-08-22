package models

import (
	"context"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi113.php's driver_show_bookings() (OnePayTaxi
// tenant, ~line 4190-4323), the model behind moddriverapi201.php's
// 'driver_booking_list' case (~line 10321-10573) when request_type == 3 -
// the "New Show Booking" broadcast list every idle driver polls to check
// whether any unassigned trip is currently available for them. This is the
// specific request_type causing load at scale (1000 drivers polling on a
// fixed interval) - request_type 1 (driver_pending_bookings, the driver's
// own assigned/upcoming trip) and request_type 2 (driver_past_bookings,
// trip history) are NOT ported here; they're not continuously polled the
// way request_type 3 is, so they stay on the legacy PHP endpoint for now.
// See controllers/driverBookingListController.go for the request_type
// dispatch and the exact response field trimming/commission math (ported
// from moddriverapi201.php:10508-10561).

// UserAuthToken is the shape of a MDB_USER_TOKEN (collection user_auth_token)
// row - only the fields this port actually reads.
type UserAuthToken struct {
	DriverID int64 `bson:"driver_id"`
}

// AuthenticateUserToken ports valid_userAuth() (modmobileapi111extended.php
// ~line 4206): looks up the driver's session token, scoped to both the
// token value AND the claimed driver id (the legacy client sends the driver
// id separately as the `i` query-string param - see
// fleeteratokenization.php's encrypt_encode_json(), which is where the
// legacy PHP itself reads $_GET['i'] for this exact check). A found row
// means the token is valid for that driver.
func AuthenticateUserToken(masterDB *mongo.Database, userAuth string, driverID int64) (*UserAuthToken, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var tok UserAuthToken
	err := masterDB.Collection(collections.USER_TOKEN).
		FindOne(ctx, bson.M{"new_user_key": userAuth, "driver_id": driverID}).
		Decode(&tok)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// DriverShowBookingRow is the shape driver_show_bookings() projects, before
// the controller's per-row commission/formatting pass.
type DriverShowBookingRow struct {
	PassengersLogID                 int64   `bson:"passengers_log_id"`
	PickupTime                      time.Time `bson:"pickup_time"`
	PickupLongitude                 float64 `bson:"pickup_longitude"`
	PickupLatitude                  float64 `bson:"pickup_latitude"`
	DropLatitude                    float64 `bson:"drop_latitude"`
	DropLongitude                   float64 `bson:"drop_longitude"`
	PickupLocation                  string  `bson:"pickup_location"`
	DropLocation                    interface{} `bson:"drop_location"`
	ApproxFare                      float64 `bson:"approx_fare"`
	Distance                        interface{} `bson:"distance"`
	ApproxDistance                  float64 `bson:"approx_distance"`
	Notes                           string  `bson:"notes"`
	TripType                        interface{} `bson:"trip_type"`
	OsTripType                      interface{} `bson:"os_trip_type"`
	CancellationFare                float64 `bson:"cancellation_fare"`
	TaxiModelID                     interface{} `bson:"taxi_modelid"`
	ModelCommissionEnable           interface{} `bson:"model_commission_enable"`
	LocalModelAdminCommission       interface{} `bson:"local_model_admin_commission"`
	RentalModelAdminCommission      interface{} `bson:"rental_model_admin_commission"`
	OutstationModelAdminCommission  interface{} `bson:"outstation_model_admin_commission"`
	DriverBeta                      float64 `bson:"driver_beta"`
	OsDayCount                      interface{} `bson:"os_day_count"`
}

// DriverShowBookings ports driver_show_bookings($company_id, $driver_id,
// $start, $limit). allowedModelIDs/accountBalance are resolved by the
// caller via GetDriverModelInfo first (mirrors the legacy function's own
// "get driver taxi model info" step, kept separate here so the controller
// can surface a clean "driver not found" response before running the
// aggregation).
func DriverShowBookings(tenantDB *mongo.Database, allowedModelIDs []int64, start, limit *int64) ([]DriverShowBookingRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	matchQuery := bson.M{
		"show_booking_all_driver": 1,
		"travel_status":           0,
		"driver_id":               0,
		"pickup_time":             bson.M{"$gt": time.Now().UTC()},
		"taxi_modelid":            bson.M{"$in": allowedModelIDs},
	}

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: matchQuery}},
		bson.D{{Key: "$lookup", Value: bson.M{
			"from":         collections.SCHEDULE_TRIPS,
			"localField":   "_id",
			"foreignField": "plogs_id",
			"as":           "schedule",
		}}},
		bson.D{{Key: "$unwind", Value: bson.M{"path": "$schedule", "preserveNullAndEmptyArrays": true}}},
		bson.D{{Key: "$match", Value: bson.M{
			"schedule.driver_id": bson.M{"$not": bson.M{"$gt": 0}},
		}}},
		bson.D{{Key: "$project", Value: bson.M{
			"passengers_log_id":                  "$_id",
			"pickup_time":                         "$pickup_time",
			"pickup_longitude":                    bson.M{"$convert": bson.M{"input": "$pickup_longitude", "to": "double", "onError": 0, "onNull": 0}},
			"pickup_latitude":                     bson.M{"$convert": bson.M{"input": "$pickup_latitude", "to": "double", "onError": 0, "onNull": 0}},
			"drop_latitude":                       bson.M{"$convert": bson.M{"input": "$drop_latitude", "to": "double", "onError": 0, "onNull": 0}},
			"drop_longitude":                      bson.M{"$convert": bson.M{"input": "$drop_longitude", "to": "double", "onError": 0, "onNull": 0}},
			"pickup_location":                     "$current_location",
			"drop_location":                       bson.M{"$ifNull": bson.A{"$drop_location", 0}},
			"approx_fare":                         bson.M{"$convert": bson.M{"input": "$approx_fare", "to": "double", "onError": 0, "onNull": 0}},
			"distance":                            bson.M{"$ifNull": bson.A{"$distance", 0}},
			"approx_distance":                     bson.M{"$convert": bson.M{"input": "$approx_distance", "to": "double", "onError": 0, "onNull": 0}},
			"notes":                               bson.M{"$ifNull": bson.A{"$notes_driver", ""}},
			"trip_type":                           "$trip_type",
			"os_trip_type":                        "$os_trip_type",
			"cancellation_fare":                   bson.M{"$convert": bson.M{"input": "$fare_info.cancellation_fare", "to": "double", "onError": 0, "onNull": 0}},
			"taxi_modelid":                        "$taxi_modelid",
			"model_commission_enable":             "$model_commission_enable",
			"local_model_admin_commission":        "$local_model_admin_commission",
			"rental_model_admin_commission":       "$rental_model_admin_commission",
			"outstation_model_admin_commission":   "$outstation_model_admin_commission",
			"driver_beta":                         bson.M{"$convert": bson.M{"input": "$driver_beta", "to": "double", "onError": 0, "onNull": 0}},
			"os_day_count":                        "$os_day_count",
		}}},
		bson.D{{Key: "$sort", Value: bson.M{"pickup_time": 1}}},
	}
	if start != nil && limit != nil {
		pipeline = append(pipeline,
			bson.D{{Key: "$skip", Value: *start}},
			bson.D{{Key: "$limit", Value: *limit}},
		)
	}

	cursor, err := tenantDB.Collection(collections.PASSENGERS_LOGS).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rows []DriverShowBookingRow
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// DriverModelInfo mirrors the "get driver taxi model info" step at the top
// of driver_show_bookings() - the driver's own assigned model plus any
// higher-end models they've opted into, combined into the $in list the
// match query filters taxi_modelid against (written as a plain $in rather
// than an $expr/$or, same rationale as the legacy PHP's own comment: $expr
// conditions can't use an index, a plain $in on a single field can).
type DriverModelInfo struct {
	AllowedModelIDs []int64
	AccountBalance  float64
}

// GetDriverModelInfo ports the driver lookup at the top of
// driver_show_bookings(): $this->mongo_db->findone(MDB_PEOPLE, ['_id' =>
// driver_id], ['taxi_mapping.model_id','accept_higher_end_model','account_balance']).
// Returns nil (not an error) when the driver doesn't exist, matching the
// legacy function's `if (empty($driver)) return [];` short-circuit.
func GetDriverModelInfo(tenantDB *mongo.Database, driverID int64) (*DriverModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID}).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	info := &DriverModelInfo{}
	seen := map[int64]bool{}
	addModelID := func(v interface{}) {
		id := toInt64(v)
		if id != 0 && !seen[id] {
			seen[id] = true
			info.AllowedModelIDs = append(info.AllowedModelIDs, id)
		}
	}

	// taxi_mapping can come back as either a bare embedded doc or an array
	// of mapping docs depending on how Mongo resolves the dotted-path
	// projection - same ambiguity documented in
	// driverAuthController.go's firstTaxiMapping(), tolerated here the
	// same way rather than assuming one shape.
	switch v := raw["taxi_mapping"].(type) {
	case bson.M:
		addModelID(v["model_id"])
	case bson.A:
		if len(v) > 0 {
			if m, ok := v[0].(bson.M); ok {
				addModelID(m["model_id"])
			}
		}
	}
	switch v := raw["accept_higher_end_model"].(type) {
	case bson.A:
		for _, m := range v {
			addModelID(m)
		}
	}
	info.AccountBalance = toFloat64(raw["account_balance"])

	return info, nil
}
