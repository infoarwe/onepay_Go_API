package controllers

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"mobileapi/database"
	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi201.php action_index()'s 'driver_login' case
// (OnePayTaxi tenant, ~line 9554-9974) plus its model dependencies in
// moddriverapi113.php/modmobileapi111extended.php/fleeteracommonmodel.php.
// Model-layer functions shared with BlueTaxi's original port
// (AuthenticateDriver, FindDriverProfile, UpdateDriverPhone,
// FindTaxiForDriver, FindActiveShift, ComputeDriverStatistics,
// FindRecentTripList, FindEmergencyContacts - all in driverAuthModel.go)
// are reused as-is, confirmed to port the same shared platform PHP both
// tenants call. OnePayTaxi-specific pieces (token issuance, shift
// creation, force_login device takeover, current-trip lookup) are in
// models/driverLoginModel.go - see that file's doc comment for what's
// deliberately not ported (Firebase push on takeover, the FORCE_SHIFTOUT
// warning branch).
//
// Auth: OnePayTaxi's mobile app was never updated to send a JWT - it
// still sends/expects the DB-backed opaque token issued here
// (models.IssueUserAuthToken, MDB_USER_TOKEN), returned as `user_key`
// (the legacy field name) rather than access_token/refresh_token.
// Dispatched via POST /driver_login with the fixed `Authorization`
// product-key header (LegacyProductAuthenticate, registered in
// routes/driverRouter.go) - NOT `authkey`/ProductAuthenticate, which is
// BlueTaxi's distinct scheme. Domain is resolved from the `Domain` header
// (ValidateDomain), same as every other per-tenant route.
//
// Legacy driver password comparison is a raw equality check against an
// MD5 hex digest the client computes and sends as-is (see
// requests.DriverLoginRequest's doc comment) - reproduced as-is here for
// compatibility with existing stored driver passwords, not as a new design
// choice.
func DriverLogin() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverLoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request ", Status: -6})
			return
		}
		if req.Phone == "" || req.Password == "" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Validation error", Status: -5})
			return
		}

		domain := c.Request.Header.Get("Domain")
		mongoDB, ok := database.DB.(*database.MongoDB)
		if !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if domain == "" || !mongoDB.IsDomainValid(domain) {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Domain", Status: -1})
			return
		}
		tenantDB, _ := mongoDB.GetDatabase(domain)

		// check_phone_people() + check_mobile_driver() + driver_login() are
		// three separate legacy queries distinguishing "phone not
		// registered" / "signup incomplete" / "wrong password" - collapsed
		// into the one AuthenticateDriver lookup, same rationale as
		// BlueTaxi's port (see driverAuthModel.go's file doc comment).
		record, err := models.AuthenticateDriver(tenantDB, req.Phone, 0)
		if err != nil {
			log.Println("driver_login: AuthenticateDriver error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if record == nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "This phone number is not registered", Status: 2})
			return
		}

		driverID := toInt64(record["_id"])
		companyID := record["company_id"]

		if signupStatus := int(toInt64(record["signup_status"])); signupStatus == 1 || signupStatus == 2 || signupStatus == 3 {
			c.JSON(http.StatusOK, gin.H{
				"message":       "Registration not yet completed",
				"status":        100,
				"signup_status": signupStatus,
				"driver_id":     driverID,
				"company_id":    companyID,
			})
			return
		}

		userStatus := toString(record["status"])
		if userStatus == "T" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Your account has been deactivated", Status: 0})
			return
		}

		loginStatus := toString(record["login_status"])
		loginFrom := toString(record["login_from"])
		deviceID := toString(record["device_id"])

		if toString(record["password"]) != req.Password {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid password", Status: -1})
			return
		}

		profile, err := models.FindDriverProfile(tenantDB, driverID)
		if err != nil {
			log.Println("driver_login: FindDriverProfile error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if profile == nil {
			// No currently-active taxi_mapping window - the legacy PHP would
			// carry on with a mostly-empty $driver_details[0] here too
			// rather than fail the login outright, so this does the same.
			profile = bson.M{}
		}

		driverInfo := subDoc(profile, "driverinfo")
		freeStatus := toString(driverInfo["status"])
		if freeStatus == "" {
			freeStatus = "F"
		}
		if currentTripArr, ok := profile["current_trip"].(bson.A); ok && len(currentTripArr) > 0 {
			freeStatus = "B"
		}

		// Another device is currently signed in as this driver.
		deviceConflict := loginStatus == "S" && loginFrom == "D" && deviceID != req.DeviceID
		if deviceConflict {
			if !req.ForceLogin {
				c.JSON(http.StatusOK, response.LegacyResponse{Message: "Driver already logged in another device", Status: 0})
				return
			}
			if freeStatus != "F" && freeStatus != "" {
				c.JSON(http.StatusOK, response.LegacyResponse{Message: "Driver is currently on a trip on another device", Status: -1})
				return
			}
			if err := models.ForceLoginTakeover(tenantDB, driverID, req.DeviceID, req.DeviceToken, req.DeviceType); err != nil {
				log.Println("driver_login: ForceLoginTakeover error:", err)
				c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
				return
			}
			// The Firebase push kicking the old device is deliberately not
			// sent here - see models/driverLoginModel.go's doc comment.
		}

		taxiID, err := models.FindTaxiForDriver(tenantDB, driverID)
		if err != nil {
			log.Println("driver_login: FindTaxiForDriver error:", err)
		}
		if taxiID == nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "No taxi is currently assigned to you", Status: -3})
			return
		}

		if !deviceConflict {
			// Normal path (no other-device conflict to resolve): register
			// this device/session directly, matching update_driver_phone()'s
			// field set in the legacy 'else' branch.
			if err := models.UpdateDriverPhone(tenantDB, driverID, req.DeviceID, req.DeviceToken, req.DeviceType, "S"); err != nil {
				log.Println("driver_login: UpdateDriverPhone error:", err)
			}
		}

		// Both the force_login-takeover and normal paths open a fresh shift
		// and report shift_status "IN" in the legacy response - reproduced
		// unconditionally here for both, rather than replicating
		// get_driver_currentshift()'s confirmed field-mapping bug that
		// (accidentally) makes the legacy force_login path skip opening one
		// (see driverLoginModel.go's file doc comment on this policy).
		shiftID, err := models.InsertDriverShift(tenantDB, driverID, taxiID)
		if err != nil {
			log.Println("driver_login: InsertDriverShift error:", err)
		}

		siteInfo, err := models.FindSiteInfo(tenantDB)
		if err != nil {
			log.Println("driver_login: FindSiteInfo error:", err)
		}
		var timezoneField interface{}
		if siteInfo != nil {
			timezoneField = siteInfo["timezone"]
		}
		_, loc := resolveTenantLocation(timezoneField)

		now := time.Now().In(loc)
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UTC()
		currentTrip, err := models.GetDriverCurrentTrip(tenantDB, driverID, dayStart)
		if err != nil {
			log.Println("driver_login: GetDriverCurrentTrip error:", err)
		}
		driverStatus := models.DeriveDriverStatus(currentTrip.TravelStatus)

		stats, err := models.ComputeDriverStatistics(tenantDB, driverID, loc)
		if err != nil {
			log.Println("driver_login: ComputeDriverStatistics error:", err)
		}

		recentTripList, err := models.FindRecentTripList(tenantDB, driverID)
		if err != nil {
			log.Println("driver_login: FindRecentTripList error:", err)
			recentTripList = []bson.M{}
		}
		sosDetail, err := models.FindEmergencyContacts(tenantDB, driverID)
		if err != nil {
			log.Println("driver_login: FindEmergencyContacts error:", err)
			sosDetail = []interface{}{}
		}

		userKey, err := models.IssueUserAuthToken(tenantDB, driverID, req.Phone, req.DeviceID, req.DeviceToken)
		if err != nil {
			log.Println("driver_login: IssueUserAuthToken error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}

		driverDetails := flattenDriverProfile(profile)
		driverDetails["userid"] = driverID
		driverDetails["shift_status"] = "IN"
		driverDetails["shiftupdate_id"] = shiftID
		driverDetails["taxi_id"] = taxiID
		driverDetails["trip_id"] = currentTrip.PassengersLogID
		driverDetails["travel_status"] = currentTrip.TravelStatus
		driverDetails["driver_status"] = driverStatus
		driverDetails["driver_first_login"] = record["driver_first_login"]
		driverDetails["driver_wallet"] = shiftID
		bankID := toString(driverDetails["bank_id"])
		driverDetails["bank_id_status"] = 0
		if bankID != "" {
			driverDetails["bank_id_status"] = 1
		}
		if siteInfo != nil {
			if v, ok := siteInfo["driver_threshold_setting"]; ok {
				driverDetails["driver_threshold_setting"] = v
			}
			if v, ok := siteInfo["driver_threshold_amount"]; ok {
				driverDetails["driver_threshold_amount"] = v
			}
		}
		driverDetails["driver_statistics"] = gin.H{
			"total_trip":             stats.CompletedTrips + stats.RejectedTrips + stats.CancelledTrips,
			"completed_trip":         stats.CompletedTrips,
			"total_earnings":         fmt.Sprintf("%.2f", stats.TotalEarnings),
			"overall_rejected_trips": stats.RejectedTrips,
			"cancelled_trips":        stats.CancelledTrips,
			"today_earnings":         fmt.Sprintf("%.2f", stats.TodayEarnings),
			"shift_status":           "IN",
			"time_driven":            stats.TimeDrivenToday,
			"status":                 1,
		}
		if driverFirstLogin := int(toInt64(record["driver_first_login"])); driverFirstLogin == 1 && userStatus == "A" {
			if err := models.ChangeDriverFirstLoginFlag(tenantDB, driverID); err != nil {
				log.Println("driver_login: ChangeDriverFirstLoginFlag error:", err)
			}
		}

		// A deactivated ('D') account is still allowed through the whole
		// flow above (shift opened, session registered) - only the final
		// message differs, matching the legacy driver_type=='D' branch.
		message := "You have logged in successfully"
		status := 1
		if userStatus == "D" {
			if signupStatus := int(toInt64(record["signup_status"])); signupStatus == 0 {
				message = "Your account is not active"
				status = 10
			} else {
				message = "Your account is waiting for admin approval"
				status = 20
			}
		}

		resp := gin.H{
			"message":          message,
			"status":           status,
			"detail":           gin.H{"driver_details": []bson.M{driverDetails}},
			"recent_trip_list": recentTripList,
			"user_key":         userKey,
		}
		if status == 1 {
			resp["sos_detail"] = sosDetail
		}
		if siteInfo != nil {
			if v, ok := siteInfo["driver_threshold_setting"]; ok {
				resp["driver_threshold_setting"] = v
			}
			if v, ok := siteInfo["driver_threshold_amount"]; ok {
				resp["driver_threshold_amount"] = v
			}
		}
		resp["driver_wallet"] = shiftID

		c.JSON(http.StatusOK, resp)
	}
}

// DriverChangePassword ports moddriverapi201.php action_index()'s
// 'driver_change_password' case (~line 18549-18565) plus change_password()
// (moddriverapi113.php:3546-3553).
//
// Auth: the legacy request carries `driver_id` directly in the body and
// trusts it outright - no ownership check against the session at all (the
// `userAuth`/authkey headers in the sample request are just the standard
// per-call auth wrapper, not cross-checked against the target driver_id).
// That's a real gap in the legacy version: with a valid session for ANY
// driver, you can pass a different driver_id and change THEIR password.
// This port does not reproduce that gap - the target driver is the one
// authenticated via DriverAuthenticate's JWT (`userAuth` header), and the
// body's `driver_id` is accepted for wire compatibility but ignored as an
// authority, same pattern as driver_location_history_update.
func DriverChangePassword() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverChangePasswordRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request ", Status: 0})
			return
		}

		dbVal, exists := c.Get("db")
		db, ok := dbVal.(*mongo.Database)
		if !exists || !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 0})
			return
		}

		driverIDVal, _ := c.Get("driver_id")
		driverID := parseInt64(toStringAny(driverIDVal))
		if driverID == 0 {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "no_data", Status: 0})
			return
		}

		if req.Password != req.ConfirmPassword {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Password and Confirm Password do not match", Status: 0})
			return
		}
		if req.Password == "" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "no_data", Status: 0})
			return
		}

		if err := models.ChangeDriverPassword(db, driverID, req.Password); err != nil {
			log.Println("driver_change_password: ChangeDriverPassword error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 0})
			return
		}

		c.JSON(http.StatusOK, response.LegacyResponse{Message: "success", Status: 1})
	}
}

// flattenDriverProfile merges FindDriverProfile's joined company/driver_ref
// sub-documents onto the top level (same rationale as flattenSiteInfo),
// leaving the raw people-doc fields as the base layer.
func flattenDriverProfile(profile bson.M) bson.M {
	detail := bson.M{}
	for k, v := range profile {
		if k == "_id" || k == "company" || k == "driver_ref" || k == "driverinfo" || k == "taxi_mapping" {
			continue
		}
		detail[k] = v
	}
	if company := subDoc(profile, "company"); len(company) > 0 {
		if companyDetails := subDoc(company, "companydetails"); len(companyDetails) > 0 {
			detail["company_address"] = companyDetails["company_address"]
			detail["bankname"] = companyDetails["bankname"]
			detail["bankaccount_no"] = companyDetails["bankaccount_no"]
			detail["company_ownerid"] = companyDetails["userid"]
		}
	}
	if ref := subDoc(profile, "driver_ref"); len(ref) > 0 {
		detail["driver_wallet_amount"] = toString(ref["registered_driver_wallet"])
	}
	if mapping := firstTaxiMapping(profile); len(mapping) > 0 {
		detail["taxi_no"] = mapping["taxi_no"]
		detail["model_id"] = mapping["model_id"]
		detail["model_name"] = mapping["model_name"]
		detail["mapping_startdate"] = mapping["mapping_startdate"]
		detail["mapping_enddate"] = mapping["mapping_enddate"]
	}
	return detail
}

// firstTaxiMapping normalizes people.taxi_mapping to a single bson.M.
// Unlike CheckDriverDevice's aggregation (driverLocationModel.go), which
// $unwinds this field before matching, FindDriverProfile's pipeline mirrors
// the legacy driver_profile() as researched - it matches directly on the
// dotted taxi_mapping.* path without unwinding first, so the field can come
// back as either a bare embedded object or an array of mapping objects
// depending on how MongoDB resolves that dotted-path projection; this
// tolerates both rather than assuming one.
func firstTaxiMapping(profile bson.M) bson.M {
	switch v := profile["taxi_mapping"].(type) {
	case bson.M:
		return v
	case bson.A:
		if len(v) > 0 {
			if m, ok := v[0].(bson.M); ok {
				return m
			}
		}
	}
	return bson.M{}
}
