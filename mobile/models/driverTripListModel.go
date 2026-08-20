package models

import (
	"context"
	"math"
	"time"

	"mobileapi/collections"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// This file ports moddriverapi201.php action_index()'s
// 'driver_recent_trip_list' case (BlueTaxi tenant, ~line 4723-5034) and its
// model dependencies. See controllers/driverTripListController.go for the
// full flow and the fields intentionally NOT ported.
//
// Scope/fidelity notes:
//   - GetDriverLoginSummary (today/monthly login+peak hours) is a
//     best-effort reimplementation of a genuinely distinct subsystem
//     (fleeteracommonmodel.php:10775-11480, ~700 lines) built on
//     driver_shift_history + driver_daily_logins. The exact internals of
//     the legacy split_peak_seconds()/get_driver_daily_login_hours() were
//     described but not quoted verbatim by the research this was ported
//     from, and driver_daily_logins' exact per-day document shape (the
//     field holding the day, in particular) isn't confirmed against live
//     data - this queries by driver_id only and filters/buckets by day in
//     Go instead of trusting a specific Mongo-side date field type.
//   - DriverHeatmap ports driver_heatmap() faithfully (query shape is
//     simple and fully traced), but the tenant-configured
//     driver_heatmap_seconds threshold defaults to 0, which makes the
//     result empty in practice for most tenants - matches the sample
//     response's `"driver_heatmap":[]`.
//   - The document-expiry status override (get_expiry_dates_product(),
//     which can replace the whole response with status 41 and an
//     "X got expired" message) is NOT ported - it checks up to 7 expiry
//     date fields across driver/taxi docs whose exact field names weren't
//     confirmed against live data, and guessing them wrong would silently
//     produce false "your license expired" responses, worse than omitting
//     the check entirely.
//   - The PACKAGE_TYPE==3||0 subscription-plan/commission-wallet gate
//     (which can early-return status -2/-3) is NOT ported - it's specific
//     to a tenant billing mode not confirmed for this tenant.
//   - driver_logged_status()/driver_login_status() (the legacy "is this
//     session still valid" / "is the driver currently active" checks) are
//     not reproduced as DB queries - session validity is already
//     established by DriverAuthenticate's JWT before this code runs; the
//     "deactivated driver" gate is reproduced via people.status == 'D'
//     instead (see controllers/driverTripListController.go).

// GetDriverInfo ports getDriverInfo(): the driver fields needed before the
// main response is assembled (status gate, rejection count, accepted
// higher-end models, mapped taxi id, app version code).
func GetDriverInfo(tenantDB *mongo.Database, driverID int64) (bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID, "user_type": "D"}, options.FindOne().SetProjection(bson.M{
			"status": 1, "total_rejection_count": 1, "accept_higher_end_model": 1,
			"taxi_id": 1, "driverinfo": 1,
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

// ResetTodayRejectionCountIfZero ports reset_today_rejection_counts():
// self-healing reset of the cached people.total_rejection_count counter
// when today's driver_rejection_list count is actually zero (doesn't
// correct a stale nonzero value, matching the legacy behavior exactly).
func ResetTodayRejectionCountIfZero(tenantDB *mongo.Database, driverID int64, dayStart, dayEnd time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := tenantDB.Collection(collections.REJECTION_HISTORY).CountDocuments(ctx, bson.M{
		"driver_id": driverID, "createdate": bson.M{"$gte": dayStart, "$lt": dayEnd},
	})
	if err != nil {
		return err
	}
	if count != 0 {
		return nil
	}

	_, err = tenantDB.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{"total_rejection_count": 0}},
	)
	return err
}

// GetDriverWallet ports get_driver_wallet(): the driver's account_balance.
func GetDriverWallet(tenantDB *mongo.Database, driverID int64) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID}, options.FindOne().SetProjection(bson.M{"account_balance": 1})).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return toFloat64(raw["account_balance"]), nil
}

// GetDriverBookingLimit ports get_driver_comp_notification()'s
// booking_limit field.
func GetDriverBookingLimit(tenantDB *mongo.Database, driverID int64) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.PEOPLE).
		FindOne(ctx, bson.M{"_id": driverID, "user_type": "D"}, options.FindOne().SetProjection(bson.M{"booking_limit": 1})).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return toFloat64(raw["booking_limit"]), nil
}

// CountBookingsToday ports book_limit(): today's completed-trip count for
// this driver, excluding booking_from==2.
func CountBookingsToday(tenantDB *mongo.Database, driverID int64, dayStart time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return tenantDB.Collection(collections.PASSENGERS_LOGS).CountDocuments(ctx, bson.M{
		"driver_id": driverID, "travel_status": 1,
		"createdate":   bson.M{"$gte": dayStart},
		"booking_from": bson.M{"$ne": 2},
	})
}

// GetActiveDriverNoticeList ports get_active_driver_notice_list().
func GetActiveDriverNoticeList(tenantDB *mongo.Database) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	cursor, err := tenantDB.Collection(collections.DRIVER_NOTICE).Find(ctx, bson.M{
		"status": "A", "start_date": bson.M{"$lte": now}, "end_date": bson.M{"$gte": now},
	}, options.Find().SetProjection(bson.M{"message": 1, "start_date": 1, "end_date": 1}))
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

// DriverHeatmap ports driver_heatmap(): recently-active online passengers
// (a demand heatmap), filtered by the tenant's driver_heatmap_seconds
// staleness threshold - defaults to 0 in most tenants, which makes this
// empty in practice (matches the sample response).
func DriverHeatmap(tenantDB *mongo.Database, thresholdSeconds int64) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := tenantDB.Collection(collections.PASSENGERS).Find(ctx, bson.M{
		"user_status": "A", "loc.coordinates": bson.M{"$exists": true},
	}, options.Find().SetProjection(bson.M{"loc": 1, "updated_date": 1}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var raw []bson.M
	if err := cursor.All(ctx, &raw); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	out := []bson.M{}
	for _, r := range raw {
		updated, ok := toTime(r["updated_date"])
		if !ok {
			continue
		}
		if now.Sub(updated).Seconds() > float64(thresholdSeconds) {
			continue
		}
		loc := bsonSub(r, "loc")
		coords, ok := loc["coordinates"].(bson.A)
		if !ok || len(coords) < 2 {
			continue
		}
		out = append(out, bson.M{"latitude": coords[1], "longitude": coords[0]})
	}
	return out, nil
}

// GetModelEarningMinimum ports get_model_earning_minimum(): the monthly
// earning target configured on the driver's mapped taxi model.
func GetModelEarningMinimum(tenantDB *mongo.Database, modelID interface{}) (float64, error) {
	if modelID == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var raw bson.M
	err := tenantDB.Collection(collections.MOTOR_MODEL).
		FindOne(ctx, bson.M{"_id": modelID}, options.FindOne().SetProjection(bson.M{"month_earning_minimum": 1})).
		Decode(&raw)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return toFloat64(raw["month_earning_minimum"]), nil
}

// GetDriverNoLeavePenaltyTotal ports get_driver_no_leave_penalty_total():
// sum of this month's no-leave penalties, deducted from the earning target.
func GetDriverNoLeavePenaltyTotal(tenantDB *mongo.Database, driverID int64, monthStart, monthEnd time.Time) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"driver_id": driverID, "status": bson.M{"$ne": "T"},
			"date": bson.M{"$gte": monthStart, "$lte": monthEnd},
		}}},
		{{Key: "$group", Value: bson.M{"_id": nil, "total": bson.M{"$sum": "$penalty_amount"}}}},
	}
	cursor, err := tenantDB.Collection(collections.DRIVER_NO_LEAVE_PENALTY).Aggregate(ctx, pipeline)
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

// DriverEarningsSummary mirrors getTodayDriverEarnings()'s result shape.
type DriverEarningsSummary struct {
	TotalAmount   float64
	TotalTrips    int64
	AverageRating float64
	AverageAmount float64
}

// GetDriverEarnings ports getTodayDriverEarnings($driverId, $start, $end) -
// called twice by the caller (today's window, and the month-to-date
// window) to produce both the "today" and "monthly" halves of the
// response. Simplified vs. the legacy version: always includes
// used_wallet_amount in the fare sum, rather than conditionally excluding
// it based on the tenant's PASS_SIDE_PAYMENT setting (that flag's actual
// values/meaning weren't confirmed against live data).
func GetDriverEarnings(tenantDB *mongo.Database, driverID int64, start, end time.Time) (DriverEarningsSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var summary DriverEarningsSummary

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"driver_id": driverID, "travel_status": 1,
			"actual_pickup_time": bson.M{"$gte": start, "$lte": end},
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$transaction_info", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$group", Value: bson.M{
			"_id":          nil,
			"total_amount": bson.M{"$sum": bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$transaction_info.amt", 0}}, bson.M{"$ifNull": bson.A{"$used_wallet_amount", 0}}}}},
			"total_trips":  bson.M{"$sum": 1},
			"total_rating": bson.M{"$sum": bson.M{"$ifNull": bson.A{"$rating", 0}}},
			"rated_trips":  bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$ifNull": bson.A{"$rating", false}}, 1, 0}}},
		}}},
	}
	cursor, err := tenantDB.Collection(collections.PASSENGERS_LOGS).Aggregate(ctx, pipeline)
	if err != nil {
		return summary, err
	}
	defer cursor.Close(ctx)

	var results []bson.M
	if err := cursor.All(ctx, &results); err != nil {
		return summary, err
	}
	if len(results) == 0 {
		return summary, nil
	}

	r := results[0]
	summary.TotalAmount = toFloat64(r["total_amount"])
	summary.TotalTrips = toInt64(r["total_trips"])
	if ratedTrips := toInt64(r["rated_trips"]); ratedTrips > 0 {
		summary.AverageRating = round2(toFloat64(r["total_rating"]) / float64(ratedTrips))
	}
	if summary.TotalTrips > 0 {
		summary.AverageAmount = round2(summary.TotalAmount / float64(summary.TotalTrips))
	}
	return summary, nil
}

// GetTodayTripInfo ports getTodayTripInfo(): today's raw trip count
// (regardless of status, unlike GetDriverEarnings) and whether the driver
// has an approved selfie logged today.
func GetTodayTripInfo(tenantDB *mongo.Database, driverID int64, start, end time.Time) (totalTripCount int64, selfieTake int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	totalTripCount, err = tenantDB.Collection(collections.PASSENGERS_LOGS).CountDocuments(ctx, bson.M{
		"driver_id": driverID, "createdate": bson.M{"$gte": start, "$lte": end},
	})
	if err != nil {
		return 0, 0, err
	}

	selfieCount, err := tenantDB.Collection(collections.DRIVER_SELFIE).CountDocuments(ctx, bson.M{
		"driver_id": driverID, "status": "A", "selfie_datetime": bson.M{"$gte": start, "$lte": end},
	})
	if err != nil {
		return totalTripCount, 0, err
	}
	if selfieCount > 0 {
		selfieTake = 1
	}
	return totalTripCount, selfieTake, nil
}

// GetRecentDriverTripList ports get_recent_driver_trip_list(): the
// driver's 3 most recent completed/pending-settlement trips. noImageURL is
// used whenever a trip has no profile_image on file (the legacy version
// checks a local file's existence before falling back; this always uses
// the fallback when the field is blank, since this Go service doesn't
// necessarily share a filesystem with the PHP app's upload directory).
func GetRecentDriverTripList(tenantDB *mongo.Database, driverID int64, noImageURL string, loc *time.Location) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	match := bson.M{
		"driver_id": driverID, "travel_status": bson.M{"$in": bson.A{9, 1}},
		"drop_time": bson.M{"$ne": nil},
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$sort", Value: bson.D{{Key: "drop_time", Value: -1}}}},
		{{Key: "$limit", Value: int64(3)}},
		{{Key: "$lookup", Value: bson.M{
			"from": collections.MOTOR_MODEL, "localField": "taxi_modelid", "foreignField": "_id", "as": "motor_model",
		}}},
		{{Key: "$unwind", Value: bson.M{"path": "$motor_model", "preserveNullAndEmptyArrays": true}}},
		{{Key: "$unwind", Value: bson.M{"path": "$transaction_info", "preserveNullAndEmptyArrays": true}}},
	}

	cursor, err := tenantDB.Collection(collections.PASSENGERS_LOGS).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var raw []bson.M
	if err := cursor.All(ctx, &raw); err != nil {
		return nil, err
	}

	out := make([]bson.M, 0, len(raw))
	for _, doc := range raw {
		motorModel := bsonSub(doc, "motor_model")
		txn := bsonSub(doc, "transaction_info")

		fare := toFloat64(txn["amt"])
		if usedWallet := toFloat64(doc["used_wallet_amount"]); usedWallet != 0 {
			fare = toFloat64(doc["admin_amount"]) + toFloat64(doc["driver_amount"])
		}

		profileImage := toString(doc["profile_image"])
		if profileImage == "" {
			profileImage = noImageURL
		}

		dropTime := ""
		if t, ok := toTime(doc["drop_time"]); ok {
			dropTime = t.In(loc).Format("02-01-2006 15:04:05")
		}

		out = append(out, bson.M{
			"drop_time":       dropTime,
			"trip_id":         doc["_id"],
			"fare":            round2(fare),
			"payment_type":    doc["payment_type"],
			"pickup_location": doc["current_location"],
			"drop_location":   doc["drop_location"],
			"model_name":      motorModel["model_name"],
			"profile_image":   profileImage,
		})
	}
	return out, nil
}

// UpdateDriverDeviceToken ports the conditional update_driver_people(...)
// call: persists the device token when the client sends one.
func UpdateDriverDeviceToken(tenantDB *mongo.Database, driverID int64, deviceToken string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := tenantDB.Collection(collections.PEOPLE).UpdateOne(
		ctx,
		bson.M{"_id": driverID},
		bson.M{"$set": bson.M{"device_token": deviceToken}},
	)
	return err
}

// DriverLoginSummary mirrors get_driver_login_summary()'s result shape,
// in decimal hours.
type DriverLoginSummary struct {
	TodayLoginHours   float64
	TodayPeakHours    float64
	MonthlyLoginHours float64
	MonthlyPeakHours  float64
}

// GetDriverLoginSummary is a best-effort port of the login/peak-hours
// subsystem (get_driver_login_summary -> get_driver_daily_login_hours ->
// split_peak_seconds) - see the file doc comment for the fidelity caveat.
// Sums driver_daily_logins' stored per-day totals for the current month,
// separates out "today"'s stored total, and - if the driver currently has
// an open shift (driver_shift_history, shift_end == nil) - adds that
// shift's live in-progress duration on top, same as the legacy version
// reflecting real-time progress instead of only closed shifts.
func GetDriverLoginSummary(tenantDB *mongo.Database, driverID int64, loc *time.Location) (DriverLoginSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var summary DriverLoginSummary

	now := time.Now().In(loc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	cursor, err := tenantDB.Collection(collections.DRIVER_DAILY_LOGIN).Find(ctx, bson.M{"driver_id": driverID})
	if err != nil {
		return summary, err
	}
	defer cursor.Close(ctx)

	var days []bson.M
	if err := cursor.All(ctx, &days); err != nil {
		return summary, err
	}

	var monthlyTotalSeconds, monthlyPeakSeconds, todayTotalSeconds, todayPeakSeconds float64
	for _, day := range days {
		docDate, ok := toTime(day["date"])
		if !ok {
			continue
		}
		docDate = docDate.In(loc)
		if docDate.Before(monthStart) || docDate.After(now) {
			continue
		}
		totalSeconds := toFloat64(day["total_seconds"])
		peakSeconds := toFloat64(day["total_peak_seconds"])
		monthlyTotalSeconds += totalSeconds
		monthlyPeakSeconds += peakSeconds
		if !docDate.Before(todayStart) {
			todayTotalSeconds += totalSeconds
			todayPeakSeconds += peakSeconds
		}
	}

	openShift, err := FindActiveShift(tenantDB, driverID)
	if err != nil {
		return summary, err
	}
	if openShift != nil {
		if shiftStart, ok := toTime(openShift["shift_start"]); ok {
			liveTotal, livePeak := splitPeakSeconds(shiftStart, time.Now(), loc)
			monthlyTotalSeconds += liveTotal
			monthlyPeakSeconds += livePeak
			todayTotalSeconds += liveTotal
			todayPeakSeconds += livePeak
		}
	}

	summary.MonthlyLoginHours = round2(monthlyTotalSeconds / 3600)
	summary.MonthlyPeakHours = round2(monthlyPeakSeconds / 3600)
	summary.TodayLoginHours = round2(todayTotalSeconds / 3600)
	summary.TodayPeakHours = round2(todayPeakSeconds / 3600)
	return summary, nil
}

// splitPeakSeconds classifies the [start, end) interval into total and
// "peak" (06:00-09:00 and 17:00-21:00 local, per the hardcoded windows the
// legacy response's morning_peak_hours/evening_peak_hours strings describe)
// seconds, handling intervals that span multiple days.
func splitPeakSeconds(start, end time.Time, loc *time.Location) (total, peak float64) {
	if end.Before(start) {
		start, end = end, start
	}
	total = end.Sub(start).Seconds()

	cur := start
	for cur.Before(end) {
		dayLocal := cur.In(loc)
		dayStart := time.Date(dayLocal.Year(), dayLocal.Month(), dayLocal.Day(), 0, 0, 0, 0, loc)
		peak += overlapSeconds(start, end, dayStart.Add(6*time.Hour), dayStart.Add(9*time.Hour))
		peak += overlapSeconds(start, end, dayStart.Add(17*time.Hour), dayStart.Add(21*time.Hour))

		next := dayStart.AddDate(0, 0, 1)
		if !next.After(cur) {
			break // guard against a non-advancing loop
		}
		cur = next
	}
	return total, peak
}

func overlapSeconds(aStart, aEnd, bStart, bEnd time.Time) float64 {
	s := aStart
	if bStart.After(s) {
		s = bStart
	}
	e := aEnd
	if bEnd.Before(e) {
		e = bEnd
	}
	if e.Before(s) {
		return 0
	}
	return e.Sub(s).Seconds()
}

// toTime tolerantly converts a BSON date-ish field (primitive.DateTime, a
// Go time.Time, or a "2006-01-02"/RFC3339 string) to a time.Time.
func toTime(v interface{}) (time.Time, bool) {
	switch val := v.(type) {
	case primitive.DateTime:
		return val.Time(), true
	case time.Time:
		return val, true
	case string:
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01-02", val); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
