package models

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This file ports moddriverapi201.php action_index()'s 'driver_login' case
// (OnePayTaxi tenant, ~line 9554-9974) and its OnePayTaxi-specific model
// dependencies not already covered by driverAuthModel.go (which is reused
// as-is here: AuthenticateDriver, FindDriverProfile, UpdateDriverPhone,
// FindTaxiForDriver, FindActiveShift, ComputeDriverStatistics,
// FindRecentTripList, FindEmergencyContacts all port the SAME shared
// platform code in moddriverapi113.php/modmobileapi111extended.php that
// both tenants call - confirmed identical against OnePayTaxi's copy of
// those files before reusing).
//
// Auth/session: OnePayTaxi's mobile app was never updated to send a JWT -
// it sends the DB-backed opaque token this endpoint must issue
// (manage_userKey() in fleeteratokenization.php, action=1), written to
// MDB_USER_TOKEN (collections.USER_TOKEN) as `new_user_key`, and returned
// to the client as `user_key` (matching the legacy field name) instead of
// BlueTaxi's access_token/refresh_token.
//
// Deliberately NOT ported:
//   - update_force_login()'s Firebase push notification to the device being
//     kicked (fleeteracommonmodel.php:6885-6917) - the DB-side device
//     takeover IS ported (ForceLoginTakeover), but sending FCM pushes needs
//     a Firebase service account this Go service doesn't have configured.
//     The kicked-out device simply won't get the "someone else logged in"
//     push; its next API call will fail its own session check instead.
//   - The FORCE_SHIFTOUT warning branch (driver_login case,
//     ~line 9598-9605) - a rarely-triggered tenant policy flag combined
//     with a per-driver force_shiftout flag; not confirmed against a live
//     siteinfo field name, and skipping it just means that specific
//     warning message never fires rather than misbehaving.
//   - get_driver_currentshift()/get_driver_log_details()'s exact legacy
//     field-mapping bugs (both have confirmed bugs elsewhere in this
//     codebase - see driverAuthModel.go's file doc comment for the general
//     policy). GetDriverCurrentTrip below queries directly instead of
//     replicating those two functions' broken result shapes.

// IssueUserAuthToken ports manage_userKey($userKey, 1, $data, 'D')
// (modmobileapi111extended.php:4174-4204): mints a new opaque session
// token and inserts it into MDB_USER_TOKEN, matching the field set the
// legacy insert writes. Token format doesn't need to match the legacy
// hash('SHA256', $var_token.$rand)."_".$objectId scheme exactly - it's an
// opaque bearer value either way - but IS generated with crypto/rand
// rather than PHP's rand(), which used a single random digit as its only
// per-token entropy beyond the timestamp-ish tokenization key.
func IssueUserAuthToken(tenantDB *mongo.Database, driverID int64, driverPhone, deviceID, deviceToken string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf)
	token := hex.EncodeToString(sum[:])

	_, err := tenantDB.Collection(collections.USER_TOKEN).InsertOne(ctx, bson.M{
		"new_user_key": token,
		"driver_id":    driverID,
		"driver_phone": driverPhone,
		"deviceid":     deviceID,
		"device_token": deviceToken,
		"user_type":    "D",
		"createdate":   time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// nextID ports get_insert_id()/get_auto_id()'s "max(_id) + 1" scheme
// (modmobileapi111extended.php:66-88) - reproduced as-is including its
// race condition under concurrent inserts, since that's the existing
// production ID scheme every other write into this collection already
// uses; switching to a proper atomic counter here would just create a
// second incompatible ID sequence for the same collection.
func nextID(ctx context.Context, coll *mongo.Collection) (int64, error) {
	var doc bson.M
	err := coll.FindOne(ctx, bson.M{}, options.FindOne().
		SetSort(bson.D{{Key: "_id", Value: -1}}).
		SetProjection(bson.M{"_id": 1}),
	).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return toInt64(doc["_id"]) + 1, nil
}

// InsertDriverShift ports insert_driver_shiftservice()
// (modmobileapi111extended.php:166-185): opens a new shift record for the
// driver (shift_end nil = open, matching FindActiveShift's convention in
// driverAuthModel.go).
func InsertDriverShift(tenantDB *mongo.Database, driverID int64, taxiID interface{}) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	coll := tenantDB.Collection(collections.SHIFT_HISTORY)
	id, err := nextID(ctx, coll)
	if err != nil {
		return 0, err
	}

	_, err = coll.InsertOne(ctx, bson.M{
		"_id":         id,
		"driver_id":   driverID,
		"taxi_id":     taxiID,
		"shift_start": time.Now().UTC(),
		"shift_end":   nil,
		"reason":      nil,
		"createdate":  time.Now().UTC(),
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ForceLoginTakeover ports the DB-mutating half of update_force_login()
// (fleeteracommonmodel.php:6885-6917) - registers the new device and
// forces driverinfo.shift_status to "IN". The Firebase push to the old
// device is NOT ported - see file doc comment.
func ForceLoginTakeover(tenantDB *mongo.Database, driverID int64, deviceID, deviceToken, deviceType string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := tenantDB.Collection(collections.PEOPLE).UpdateOne(ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{
			"device_id": deviceID, "device_token": deviceToken, "device_type": deviceType,
			"account_deactivate":     0,
			"driverinfo.shift_status": "IN",
		}},
	)
	return err
}

// DriverCurrentTrip mirrors the fields the login response's driver_details
// merges from get_driver_log_details()'s (correctly-mapped) result: the
// driver's one active/payment-pending trip, if any.
type DriverCurrentTrip struct {
	PassengersLogID string
	TravelStatus    int
}

// GetDriverCurrentTrip queries directly for the driver's current
// in-progress/payment-pending trip (travel_status in 2,3,5,9 with
// driver_reply 'A', pickup_time from today onward) - same match shape as
// get_driver_log_details(), but read as this port's own typed result
// instead of replicating that function's broken $res[0] indexing (see
// file doc comment). Returns a zero-value DriverCurrentTrip (empty
// PassengersLogID) when the driver has no such trip, matching the legacy
// "no rows" case.
func GetDriverCurrentTrip(tenantDB *mongo.Database, driverID int64, dayStart time.Time) (DriverCurrentTrip, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var trip DriverCurrentTrip
	var raw bson.M
	err := tenantDB.Collection(collections.PASSENGERS_LOGS).FindOne(ctx, bson.M{
		"driver_id":     driverID,
		"driver_reply":  "A",
		"pickup_time":   bson.M{"$gte": dayStart},
		"travel_status": bson.M{"$in": bson.A{9, 5, 3, 2}},
	}, options.FindOne().SetProjection(bson.M{"_id": 1, "travel_status": 1})).Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return trip, nil
	}
	if err != nil {
		return trip, err
	}
	trip.PassengersLogID = toString(raw["_id"])
	trip.TravelStatus = int(toInt64(raw["travel_status"]))
	return trip, nil
}

// ChangeDriverFirstLoginFlag ports the `driver_first_login: 2` half of
// update_driver_people() (modmobileapi111extended.php:187-205), called
// when a driver logs in for the first time (driver_first_login == 1) and
// is already active.
func ChangeDriverFirstLoginFlag(tenantDB *mongo.Database, driverID int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := tenantDB.Collection(collections.PEOPLE).UpdateOne(ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{"driver_first_login": 2}},
	)
	return err
}

// DeriveDriverStatus ports the driver_status derivation duplicated in both
// the force_login and normal branches of the driver_login case: defaults
// to "F" (free) unless there's a current trip, in which case it reflects
// the trip's travel_status.
func DeriveDriverStatus(travelStatus int) string {
	switch travelStatus {
	case 2, 5:
		return "A"
	case 9, 3:
		return "B"
	default:
		return "F"
	}
}
