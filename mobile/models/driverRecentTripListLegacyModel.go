package models

import (
	"context"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This file ports moddriverapi201.php action_index()'s 'driver_recent_trip_list'
// case for the OnePayTaxi tenant (~line 4949-5122 of
// modules_new/modconnection/classes/controller/moddriverapi201.php) -
// distinct from and much simpler than the BlueTaxi-tenant version already
// ported in driverTripListModel.go/driverTripListController.go's
// DriverRecentTripList(). Despite the name, the legacy case never actually
// populates $trip_list (it's initialized to [] and never assigned again in
// this tenant's version) - the endpoint is really a driver home-screen
// status/gate check (active/blocked, wallet threshold, booking limit) plus
// today's earnings summary, not an actual trip listing. See
// controllers/driverRecentTripListLegacyController.go for the field-by-field
// port notes.

// GetDriverRecentInfoLegacy ports the MDB_PEOPLE reads behind
// driver_login_status()/getDriverInfo() for this case - both only ever read
// fields off the same driver document, so this is a single findOne with a
// combined projection. Returns nil (not an error) when the driver doesn't
// exist for this id/user_type, matching driver_logged_status()'s "driver
// not found" -> login_status 0 short circuit (see the controller's
// login_status handling).
func GetDriverRecentInfoLegacy(tenantDB *mongo.Database, driverID int64) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID, "user_type": "D"}, options.FindOne().SetProjection(bson.M{
			"status": 1, "accept_higher_end_model": 1,
		})).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// GetDriverTodayEarningsLegacy ports getTodayDriverEarnings()'s primary
// (non "on my way") aggregate - the only branch driver_recent_trip_list
// actually reads (total_trips/total_amount). Matches on `pickup_time`
// (confirmed against this tenant's passengers_logs schema via
// driverBookingListModel.go's DriverShowBookingRow), not
// `actual_pickup_time` as the BlueTaxi-tenant GetDriverEarnings does -
// deliberately a separate function rather than reusing that one, since the
// two tenants' schemas disagree on which timestamp field this reads.
// total_amount sums transaction_info.amt + used_wallet_amount per the
// legacy PASS_SIDE_PAYMENT==0 default (this tenant doesn't define that
// constant, so the "else" branch - always add used_wallet_amount - always
// applies). Both operands go through $convert (not just $ifNull) before the
// $add - passengers_logs stores some numeric fields as strings on certain
// rows (same issue fixed in driverBookingListModel.go's DriverShowBookings),
// and unlike a decode error, $add itself throws on a string operand and
// fails the whole aggregation.
func GetDriverTodayEarningsLegacy(tenantDB *mongo.Database, driverID int64, start, end time.Time) (totalAmount float64, totalTrips int64, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"driver_id":     driverID,
			"travel_status": 1,
			"pickup_time":   bson.M{"$gte": start, "$lte": end},
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$transaction_info", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$group", Value: bson.M{
			"_id": nil,
			"total_amount": bson.M{"$sum": bson.M{"$add": bson.A{
				bson.M{"$convert": bson.M{"input": "$transaction_info.amt", "to": "double", "onError": 0, "onNull": 0}},
				bson.M{"$convert": bson.M{"input": "$used_wallet_amount", "to": "double", "onError": 0, "onNull": 0}},
			}}},
			"total_trips": bson.M{"$sum": 1},
		}}},
	}

	cursor, aggErr := tenantDB.Collection(collections.PASSENGERS_LOGS).Aggregate(ctx, pipeline)
	if aggErr != nil {
		return 0, 0, aggErr
	}
	defer cursor.Close(ctx)

	var rows []struct {
		TotalAmount float64 `bson:"total_amount"`
		TotalTrips  int64   `bson:"total_trips"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return rows[0].TotalAmount, rows[0].TotalTrips, nil
}
