package controllers

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports driver_location_history_update_new.js - the handler the
// legacy Node service dispatches `/driver_location_history_update` to for
// tenants mobileapi_http.js, confirmed against the domain in the Postman/curl
// export this was ported from). See models/driverLocationModel.go for the
// full scope note.
//
// Auth: unlike the legacy endpoint (a device-scoped JWT carrying a device
// UUID in the `token` header, verified against a per-domain
// AES-derived secret), this uses the existing DriverAuthenticate
// middleware/tokenHelper.go session JWT (`userAuth` header, driver_id +
// company_domain claims) - the mobile app is being updated to this flow, so
// the driver identity here is trusted from the token, not the request
// body's `data.driver_id` (which the legacy client still sends but which
// this handler ignores as an authority - the JWT is authoritative).
//
// Known gap: the active-trip fare recompute (night/evening surcharge,
// waiting cost, rental/outstation formula - driver_location_history_update_new.js
// lines ~824-1077) is not ported. An active-status ('A') response reports
// distance from the client's own payload but trip_fare as 0, since a
// faithful port needs company/taxi fare-plan collections this environment
// has no live data to verify field shapes against.

// driverLocationMessages holds the plain-English literals this handler
// returns in place of the legacy i18n lookups (i18n.__(key) against
// locales/en.json), same approach as CheckCompanyDomain/GetCoreConfig.
// Two keys (logout_success, passenger_completed_trip) aren't actually
// defined in locales/en.json either - i18n would have fallen back to
// echoing the key itself - so those two are best-guess placeholder text.
var driverLocationMessages = map[string]string{
	"invalid_request":                 "Invalid Request",
	"already_login1":                  "You have already logged in another device.",
	"assigned_taxi_expired":           "Your assigned Fleet duration time has been reached OR Unassigned. Please contact company to extend",
	"logout_success":                  "You have been logged out.", // not in locales/en.json - placeholder
	"driver_history_updated":          "Driver Location history updated",
	"driver_history_already":          "Driver Location history already exists",
	"invalid_user":                    "This mobile number is not registered. Please enter a valid number.",
	"booking_cancel_message":          "Your booking ##booking_key## has been cancelled.",
	"trip_completed":                  "Your trip has been completed.",
	"trip_completed_by_driver":        "Trip completed by driver",
	"trip_completed_by_pass_card":     "Trip completed by passenger using card",
	"trip_completed_by_pass_wallet":   "Trip completed by passenger using wallet",
	"tripcompleted_admin":             "Trip has been force completed",
	"passenger_completed_trip":        "Passenger completed the trip.", // not in locales/en.json - placeholder
	"trip_cancelled_passenger":        "Trip has been cancelled by passenger.",
	"api_request_confirmed_passenger": "Booking request sent. You will receive driver confirmation shortly.",
}

func locMsg(key string) string {
	if s, ok := driverLocationMessages[key]; ok {
		return s
	}
	return key
}

func replaceBookingKey(message, tripID string) string {
	return strings.ReplaceAll(message, "##booking_key##", tripID)
}

// parseInt64 tolerantly parses request-body string fields (driver_id,
// trip_id, ...) the legacy client sends as strings - blank/non-numeric
// input resolves to 0, matching parseInt()'s NaN-as-falsy behavior at the
// call sites that read these.
func parseInt64(s string) int64 {
	i, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return i
}

func isNumericString(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// subDoc safely reads a nested sub-document off a bson.M (local twin of
// models' unexported bsonSub - kept package-local rather than exported
// across packages for a two-line helper).
func subDoc(m bson.M, key string) bson.M {
	if m == nil {
		return bson.M{}
	}
	switch sub := m[key].(type) {
	case bson.M:
		return sub
	case bson.D:
		return sub.Map()
	default:
		return bson.M{}
	}
}

// driverCurrentTripID ports the driver_current_trip_id computation inline
// in check_driver_device's success callback
// (driver_location_history_update_new.js:122-144): surfaces the trip the
// driver should resume/track, based on their current trip's travel_status.
func driverCurrentTripID(profile bson.M) int64 {
	plogs := subDoc(profile, "plogs")
	plogsID := toInt64(plogs["_id"])
	if plogsID <= 0 {
		return 0
	}
	travelStatus := plogs["travel_status"]
	driverReply := plogs["driver_reply"]
	if travelStatus == nil {
		return 0
	}
	if driverReply != nil && toString(driverReply) == "A" && toString(travelStatus) == "9" {
		return plogsID
	}
	switch toString(travelStatus) {
	case "3", "2", "5":
		return plogsID
	}
	return 0
}

// DriverLocationHistoryUpdate ports the POST /driver_location_history_update
// handler: validates the driver's device/session, persists their
// lat/lng+status, detects a stale/cancelled trip_id and alerts the driver,
// auto-dispatches a newly-assigned trip to a free driver, and (for a
// busy/active driver) reports drop-location/trip-completion state.
func DriverLocationHistoryUpdate() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverLocationHistoryUpdateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: locMsg("invalid_request"), Status: 2})
			return
		}

		dbVal, exists := c.Get("db")
		db, ok := dbVal.(*mongo.Database)
		if !exists || !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}

		driverIDStr, _ := c.Get("driver_id")
		driverID := parseInt64(toStringAny(driverIDStr))
		if driverID == 0 {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: locMsg("invalid_request"), Status: 2})
			return
		}

		data := req.Data
		driverStatus := strings.TrimSpace(data.Status)
		tripID := parseInt64(data.TripID)

		siteInfo, err := models.FindSiteInfo(db)
		if err != nil {
			log.Println("driver_location_history_update: FindSiteInfo error:", err)
		}
		cronTime := "00:00"
		notificationSeconds := 15
		if siteInfo != nil {
			if v, ok := siteInfo["cron_time"].(string); ok && v != "" {
				cronTime = v
			}
			if n := int(toInt64(siteInfo["notification_settings"])); n > 0 {
				notificationSeconds = n
			}
		}

		deviceStatus, profile, err := models.CheckDriverDevice(db, driverID, data.DeviceToken, driverStatus, cronTime)
		if err != nil {
			log.Println("driver_location_history_update: CheckDriverDevice error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if deviceStatus == models.DeviceStatusNotFound {
			if err := models.MappingExpiry(db, driverID); err != nil {
				log.Println("driver_location_history_update: MappingExpiry error:", err)
			}
			deviceStatus, profile, err = models.CheckDriverDevice(db, driverID, data.DeviceToken, driverStatus, cronTime)
			if err != nil {
				log.Println("driver_location_history_update: CheckDriverDevice retry error:", err)
				c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
				return
			}
		}

		currentTripID := driverCurrentTripID(profile)

		if deviceStatus != models.DeviceStatusOK {
			message := locMsg("assigned_taxi_expired")
			status := -15
			switch deviceStatus {
			case models.DeviceStatusAlreadyLoggedIn:
				message = locMsg("already_login1")
				status = 15
			case models.DeviceStatusLoggedOut:
				message = locMsg("logout_success")
			}
			c.JSON(http.StatusOK, gin.H{"message": message, "status": status, "current_trip_id": currentTripID})
			return
		}

		defaultRadius := data.DriverDefaultRadius
		result := gin.H{
			"message":         locMsg("driver_history_updated"),
			"status":          1,
			"default_radius":  defaultRadius,
			"current_trip_id": currentTripID,
		}

		// trip_id_valid_then: detect a stale/cancelled/completed trip_id and
		// short-circuit with a cancellation alert, same as the legacy handler.
		if tripID > 0 {
			cancelAlert := true
			if currentTrip, ok := profile["current_trip"].(bson.A); ok {
				for _, item := range currentTrip {
					if m, ok := item.(bson.M); ok && toInt64(m["trip_id"]) == tripID {
						cancelAlert = false
						break
					}
				}
			}
			langKey := "booking_cancel_message"
			if toInt64(profile["completed_trip"]) == tripID {
				langKey = "trip_completed"
			}
			if driverStatus != "" && driverStatus != "F" && !isNumericString(data.TripID) {
				cancelAlert = true
				langKey = "booking_cancel_message"
			}

			if cancelAlert {
				info, err := models.FindTripCancelInfo(db, tripID)
				if err != nil {
					log.Println("driver_location_history_update: FindTripCancelInfo error:", err)
				}
				message := locMsg(langKey)
				if info.Found && info.TravelStatus == 1 {
					switch info.PaymentType {
					case 1:
						message = locMsg("trip_completed_by_driver")
					case 2, 3:
						message = locMsg("trip_completed_by_pass_card")
					case 5:
						message = locMsg("trip_completed_by_pass_wallet")
					}
				}
				message = replaceBookingKey(message, data.TripID)
				c.JSON(http.StatusOK, gin.H{"message": message, "status": 10, "current_trip_id": currentTripID})
				return
			}
		}

		latitude, longitude := models.ParseLastLocation(data.Locations)
		now := time.Now()

		switch driverStatus {

		case "F":
			// driver_status_free_then + _check_new_request + _take_trips +
			// _take_trips_assign_to_driver
			if latitude != "0.0" && longitude != "0.0" {
				if err := models.UpdateDriverLocation(db, models.UpdateDriverLocationParams{
					DriverID: driverID, Latitude: latitude, Longitude: longitude, Status: "F",
					UpdateDate: now, Bearings: data.Bearings, Accuracy: data.Accuracy,
					Brand: data.Brand, Model: data.Model, ServiceStatus: data.ServiceStatus,
					VersionCode: data.VersionCode, CarrierName: data.CarrierName,
				}); err != nil {
					log.Println("driver_location_history_update: UpdateDriverLocation error:", err)
				}
			}

			haveAvailableTrip := int64(0)
			if currentTrip, ok := profile["current_trip"].(bson.A); ok && len(currentTrip) > 0 {
				if m, ok := currentTrip[0].(bson.M); ok {
					haveAvailableTrip = toInt64(m["trip_id"])
				}
			}
			if haveAvailableTrip > 0 {
				if details, matched := models.BuildAvailableTripDetails(profile, haveAvailableTrip, notificationSeconds); matched {
					result["message"] = locMsg("driver_history_updated")
					result["status"] = 5
					result["trip_details"] = details
				}
			}

		case "A":
			// driver_status_active_then_check_travel_status + driver_status_active_then
			plogs := subDoc(profile, "plogs")
			if toInt64(plogs["_id"]) == tripID && toInt64(plogs["travel_status"]) == 1 {
				result["message"] = locMsg("tripcompleted_admin")
				result["status"] = 7
				if plogs["passenger_trip_complete"] != nil && plogs["trip_fare"] != nil {
					result["message"] = locMsg("passenger_completed_trip")
					result["status"] = 13
					result["trip_fare"] = plogs["trip_fare"]
					result["trip_id"] = data.TripID
				}
				c.JSON(http.StatusOK, result)
				return
			}

			if err := models.UpdateDriverLocation(db, models.UpdateDriverLocationParams{
				DriverID: driverID, Latitude: latitude, Longitude: longitude, Status: "A",
				UpdateDate: now, Bearings: data.Bearings, Accuracy: data.Accuracy,
				Brand: data.Brand, Model: data.Model, ServiceStatus: data.ServiceStatus,
				VersionCode: data.VersionCode, CarrierName: data.CarrierName,
			}); err != nil {
				log.Println("driver_location_history_update: UpdateDriverLocation error:", err)
			}

			saveResult, err := models.SaveDriverLocationHistory(db, profile, driverID, tripID, data.Locations, data.Distance)
			if err != nil {
				log.Println("driver_location_history_update: SaveDriverLocationHistory error:", err)
			}
			switch saveResult.Status {
			case 1:
				result["message"] = locMsg("driver_history_updated")
				result["status"] = 1
				result["distance"] = saveResult.Distance
				// Fare recompute intentionally not ported - see file doc comment.
				result["trip_fare"] = 0
			case 2:
				result["message"] = locMsg("invalid_user")
				result["status"] = 2
			case 5:
				result["message"] = locMsg("driver_history_updated")
				result["status"] = 1
				result["distance"] = saveResult.Distance
			default:
				result["message"] = locMsg("invalid_user")
				result["status"] = -1
			}

		case "B":
			// driver_status_busy_then
			if err := models.UpdateDriverLocation(db, models.UpdateDriverLocationParams{
				DriverID: driverID, Latitude: latitude, Longitude: longitude, Status: "B",
				UpdateDate: now, Bearings: data.Bearings, Accuracy: data.Accuracy,
				Brand: data.Brand, Model: data.Model, ServiceStatus: data.ServiceStatus,
				VersionCode: data.VersionCode, CarrierName: data.CarrierName,
			}); err != nil {
				log.Println("driver_location_history_update: UpdateDriverLocation error:", err)
			}

			plogs := subDoc(profile, "plogs")
			result["message"] = locMsg("driver_history_updated")
			result["status"] = 1
			if toInt64(plogs["_id"]) == tripID {
				result["drop_location_details"] = gin.H{
					"drop_location":  plogs["drop_location"],
					"drop_latitude":  plogs["drop_latitude"],
					"drop_longitude": plogs["drop_longitude"],
				}
				if toString(plogs["driver_reply"]) == "A" && toInt64(plogs["travel_status"]) == 4 {
					result["message"] = locMsg("trip_cancelled_passenger")
					result["status"] = 7
					result["detail"] = ""
				}
			}
		}

		c.JSON(http.StatusOK, result)
	}
}

// toStringAny stringifies whatever gin.Context.Get returned for driver_id
// (a string claim in practice, but Get returns interface{}).
func toStringAny(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// toString tolerantly stringifies a BSON field value - local twin of
// models' unexported toString (same rationale as subDoc above).
func toString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return ""
	}
}
