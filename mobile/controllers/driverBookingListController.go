package controllers

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi201.php action_index()'s 'driver_booking_list'
// case (OnePayTaxi tenant, ~line 10321-10573) for request_type == 3 only -
// driver_show_bookings(), the "New Show Booking" broadcast list every idle
// driver polls to check for an unassigned trip. This is the specific
// request_type causing load at scale (1000 drivers polling on a fixed
// interval); request_type 1 (driver_pending_bookings) and 2
// (driver_past_bookings) are not continuously polled the same way and are
// NOT ported here - they return status -2 below so a caller gets a clear
// signal to keep using the legacy PHP endpoint for those, rather than a
// silently-empty list.
//
// Auth: OnePayTaxi's mobile app was never updated to send BlueTaxi's JWT
// (`userAuth` as a signed token) - it still sends the DB-backed opaque
// token issued by driver_login's manage_userKey() call, checked here via
// LegacyDriverAuthenticate (ValidateDomain + LegacyDriverAuthenticate,
// registered in routes/driverRouter.go) instead of DriverAuthenticate.
// This means driver_booking_list works against the EXISTING mobile app
// and EXISTING driver_login (still on the legacy PHP) with no app update
// and no dependency on porting driver_login to Go first.
//
// Response shape ported from moddriverapi201.php:10508-10561: the
// per-row admin-commission calculation (approx_fare/driver_beta/trip_type
// -> commission %, plus a flat driver_tax add-on read from the tenant's
// siteinfo doc - ADMIN_COMMISSON/DRIVER_TAX in the legacy PHP,
// common_config.php:804/807, both sourced from siteinfo at boot there) and
// the trimmed field set returned to the client (passengers_log_id,
// pickup_time(_text), os_trip_type, os_day_count, approx_fare,
// pickup_location, drop_location, notes, taxi_modelid, distance,
// approx_distance, cancellation_fare, approx_admin_amount, pickup/drop
// lat/long) - matches the comment already in the legacy code noting this
// was trimmed to only what the driver app reads.
//
// pickup_time/pickup_time_text are formatted in the tenant's local timezone
// (resolveTenantLocation, same helper GetCoreConfig/DriverLogin use -
// defaults to common.Config.DefaultTimezone, "Asia/Kolkata", when siteinfo
// doesn't specify one) rather than the raw stored UTC timestamp - confirmed
// against a live mismatch (admin panel showed 13:00 IST for a booking the
// driver app showed as 07:30, exactly the 5:30 UTC offset) that the earlier
// UTC-only formatting was actually reaching the driver app un-converted.
func DriverBookingList() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverBookingListRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request", Status: -1})
			return
		}

		if req.RequestType != 3 {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "This request_type is not yet served by the Go endpoint - use the legacy driver_booking_list endpoint",
				Status:  -2,
			})
			return
		}

		dbVal, exists := c.Get("db")
		db, ok := dbVal.(*mongo.Database)
		if !exists || !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}

		driverIDVal, _ := c.Get("driver_id")
		driverID, _ := driverIDVal.(int64)
		if driverID == 0 {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request", Status: -1})
			return
		}

		modelInfo, err := models.GetDriverModelInfo(db, driverID)
		if err != nil {
			log.Println("driver_booking_list: GetDriverModelInfo error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if modelInfo == nil {
			// Matches driver_show_bookings()'s `if (empty($driver)) return [];`
			c.JSON(http.StatusOK, gin.H{
				"message": "success",
				"status":  1,
				"detail":  gin.H{"pending_booking": []interface{}{}, "past_booking": []interface{}{}, "show_booking": []interface{}{}},
			})
			return
		}

		siteInfo, err := models.FindSiteInfo(db)
		if err != nil {
			log.Println("driver_booking_list: FindSiteInfo error:", err)
		}
		if siteInfo == nil {
			siteInfo = bson.M{}
		}
		defaultCommission := toFloat64Any(siteInfo["admin_commission"])
		driverTax := toFloat64Any(siteInfo["driver_tax"])
		_, loc := resolveTenantLocation(siteInfo["user_time_zone"])

		var startPtr, limitPtr *int64
		if req.Start >= 0 && req.Limit > 0 {
			startPtr, limitPtr = &req.Start, &req.Limit
		}

		rows, err := models.DriverShowBookings(db, modelInfo.AllowedModelIDs, startPtr, limitPtr)
		if err != nil {
			log.Println("driver_booking_list: DriverShowBookings error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}

		showBooking := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			commission := defaultCommission
			switch toIntAny(r.TripType) {
			case 1:
				if v := toFloat64Any(r.LocalModelAdminCommission); v != 0 {
					commission = v
				}
			case 2:
				if v := toFloat64Any(r.RentalModelAdminCommission); v != 0 {
					commission = v
				}
			case 3:
				if v := toFloat64Any(r.OutstationModelAdminCommission); v != 0 {
					commission = v
				}
			}

			adminAmount := 0.0
			if r.ApproxFare > 0 {
				adminAmount = ((r.ApproxFare - r.DriverBeta) * commission) / 100
			}
			adminAmount = round2(adminAmount + driverTax)

			showBooking = append(showBooking, gin.H{
				"passengers_log_id":   strconv.FormatInt(r.PassengersLogID, 10),
				"pickup_time":         formatPickupTime(r.PickupTime, loc),
				"pickup_time_text":    formatPickupTimeText(r.PickupTime, loc),
				"os_trip_type":        r.OsTripType,
				"os_day_count":        orZero(r.OsDayCount),
				"approx_fare":         r.ApproxFare,
				"pickup_location":     r.PickupLocation,
				"drop_location":       r.DropLocation,
				"notes":               r.Notes,
				"taxi_modelid":        r.TaxiModelID,
				"distance":            r.Distance,
				"approx_distance":     r.ApproxDistance,
				"cancellation_fare":   r.CancellationFare,
				"approx_admin_amount": adminAmount,
				"pickup_latitude":     r.PickupLatitude,
				"pickup_longitude":    r.PickupLongitude,
				"drop_latitude":       r.DropLatitude,
				"drop_longitude":      r.DropLongitude,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "success",
			"status":  1,
			"detail": gin.H{
				"pending_booking": []interface{}{},
				"past_booking":    []interface{}{},
				"show_booking":    showBooking,
			},
		})
	}
}

func formatPickupTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

func formatPickupTimeText(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("02 Jan 2006, 15:04")
}

func toIntAny(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int32:
		return int(val)
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		i, _ := strconv.Atoi(val)
		return i
	default:
		return 0
	}
}

func orZero(v interface{}) interface{} {
	if v == nil {
		return 0
	}
	return v
}
