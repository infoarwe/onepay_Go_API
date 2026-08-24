package controllers

import (
	"log"
	"net/http"
	"time"

	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi201.php action_index()'s 'driver_recent_trip_list'
// case for the OnePayTaxi tenant (~line 4949-5122) - a driver home-screen
// status/wallet/earnings-summary payload, NOT the trip listing its name
// suggests (see models/driverRecentTripListLegacyModel.go's doc comment).
// This is a DIFFERENT case from the one already ported as
// DriverRecentTripList() in driverTripListController.go, which is the
// BlueTaxi tenant's own (much richer) driver_recent_trip_list flow - the two
// tenants happen to share the /driver_recent_trip_list route path, so
// routes/driverRouter.go dispatches between this function and that one by
// which product-auth header is present (see RegisterDriverTripListRoutes).
//
// Auth: same OnePayTaxi chain as driver_login/driver_booking_list -
// LegacyProductAuthenticate (fixed `Authorization` header) + ValidateDomain
// (Domain header) + LegacyDriverAuthenticate (DB-backed opaque `userAuth`
// token, scoped to the `i` query param) - see
// middleware/legacyAuthMiddleware.go. The body's driver_id/driver_type are
// accepted for wire compatibility but only driver_type is actually read
// (see activeStatus below); the authenticated id from context is the only
// one trusted as the target driver.
//
// Deliberately NOT ported, matching this codebase's existing precedent for
// the BlueTaxi version of this case (see DriverRecentTripList()'s own doc
// comment for the same two omissions):
//   - the prehiretest/internal2 subdomain document-expiry status-41 override
//   - the PACKAGE_TYPE 3/0 subscription/commission-wallet gate (status -2/-3)
//     that only applies to enterprise-package tenants
//
// Fidelity note: get_company_time_details()'s per-company timezone lookup
// (keyed off the driver's company_id) is not ported - this reuses the
// tenant-wide siteinfo timezone (resolveTenantLocation) already used
// elsewhere in this package, same simplification driver_booking_list's
// pickup_time formatting already made for the same reason (no shared
// per-company-timezone helper exists in this Go service yet). Also uses
// local-midnight-to-next-local-midnight for "today" rather than the legacy
// 00:00:01-23:59:59 bounds - a 2-second difference at the day boundary that
// doesn't affect real trip data.
func DriverRecentTripListLegacy() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverRecentTripListRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request", Status: -1})
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

		driverInfo, err := models.GetDriverRecentInfoLegacy(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): GetDriverRecentInfoLegacy error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		// Matches driver_logged_status()'s "driver not found for this
		// id/user_type" -> login_status 0 short circuit.
		if driverInfo == nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "You have not logged in. Please log in for further process.", Status: -1})
			return
		}

		if req.DeviceToken != "" {
			if err := models.UpdateDriverDeviceToken(db, driverID, req.DeviceToken); err != nil {
				log.Println("driver_recent_trip_list (legacy): UpdateDriverDeviceToken error:", err)
			}
		}

		// driver_login_status(): active_status is "A" when people.status ==
		// 'A', otherwise falls back to the client-supplied driver_type from
		// the request body - an odd legacy fallback, but that's genuinely
		// what the PHP does.
		activeStatus := req.DriverType
		if toString(driverInfo["status"]) == "A" {
			activeStatus = "A"
		}
		if activeStatus == "D" {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "Your account is not yet activated, please contact your administrator.",
				Status:  10,
			})
			return
		}

		siteInfo, err := models.FindSiteInfo(db)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): FindSiteInfo error:", err)
		}
		if siteInfo == nil {
			siteInfo = bson.M{}
		}
		_, loc := resolveTenantLocation(siteInfo["timezone"])
		now := time.Now().In(loc)
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UTC()
		dayEnd := dayStart.AddDate(0, 0, 1)

		driverWallet, err := models.GetDriverWallet(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): GetDriverWallet error:", err)
		}

		totalAmount, totalTrips, err := models.GetDriverTodayEarningsLegacy(db, driverID, dayStart, dayEnd)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): GetDriverTodayEarningsLegacy error:", err)
		}

		// modelInfo.AllowedModelIDs is people.taxi_mapping.model_id (the
		// driver's own assigned model) merged with people.accept_higher_end_model
		// (models they've separately opted into), deduped - the same $in
		// list driver_show_bookings' own query filters taxi_modelid
		// against (see GetDriverModelInfo in driverBookingListModel.go).
		// Used here for two things: the accept_higher_end_model field below
		// (merged rather than accept_higher_end_model alone, since the app
		// wants "which models can this driver see bookings for", not just
		// the opt-in list) and show_booking_count.
		modelInfo, err := models.GetDriverModelInfo(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): GetDriverModelInfo error:", err)
			modelInfo = &models.DriverModelInfo{}
		} else if modelInfo == nil {
			modelInfo = &models.DriverModelInfo{}
		}

		// show_booking_count: how many unassigned trips (request_type=3 /
		// driver_show_bookings in driver_booking_list) are currently
		// available for this driver's taxi model(s) - a badge count for the
		// home screen, not part of the legacy PHP response but requested
		// alongside this port.
		showBookingCount, err := models.CountDriverShowBookings(db, modelInfo.AllowedModelIDs)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): CountDriverShowBookings error:", err)
		}

		result := gin.H{
			"message":                 "Drive Recent Trip List",
			"status":                  1,
			"trip_list":               []interface{}{},
			"total_trips":             totalTrips,
			"total_amount":            formatAmount(totalAmount),
			"driver_wallet":           driverWallet,
			"accept_higher_end_model": modelIDsToIDObjects(modelInfo.AllowedModelIDs),
			"show_booking_count":      showBookingCount,
		}

		bookingLimitConfigured, err := models.GetDriverBookingLimit(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): GetDriverBookingLimit error:", err)
		}
		bookedToday, err := models.CountBookingsToday(db, driverID, dayStart)
		if err != nil {
			log.Println("driver_recent_trip_list (legacy): CountBookingsToday error:", err)
		}
		bookingLimit := bookingLimitConfigured - float64(bookedToday)

		// Driver Wallet Low Alert - the three overrides below are checked
		// unconditionally in the legacy code (guarded there by "status != -1",
		// which is always true at this point since the -1/10 branches above
		// already returned). Message text for the two booking_limit_* cases
		// isn't confirmed against a live i18n dump (the translation key
		// wasn't found in any loaded language file); the wallet-low text is
		// confirmed (modsos/i18n/endef.php).
		thresholdEnabled := toString(siteInfo["driver_threshold_setting"]) == "1"
		thresholdAmount := toFloat64Any(siteInfo["driver_threshold_amount"])
		if bookingLimit <= 0 {
			result["message"] = "Booking limit exceeded for today"
			result["status"] = -4
		}
		if thresholdEnabled && driverWallet <= thresholdAmount {
			result["message"] = "Wallet amount is low,kindly recharge !"
			result["status"] = -3
		}
		if thresholdEnabled && bookingLimit <= 0 && driverWallet <= thresholdAmount {
			result["message"] = "Booking limit exceeded and wallet amount is low"
			result["status"] = -4
		}

		c.JSON(http.StatusOK, result)
	}
}

// modelIDsToIDObjects wraps each id as {"id": ...} - the wire shape
// moddriverapi201.php:5078-5086's own accept_higher_end_model trim uses
// ("only .id is read by the app"), kept for this merged
// (taxi_mapping.model_id + accept_higher_end_model) list too.
func modelIDsToIDObjects(ids []int64) []gin.H {
	out := make([]gin.H, 0, len(ids))
	for _, id := range ids {
		out = append(out, gin.H{"id": id})
	}
	return out
}
