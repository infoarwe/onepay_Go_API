package controllers

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"mobileapi/database"
	helper "mobileapi/helpers"
	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi201.php action_index()'s 'driver_login' case
// (BlueTaxi tenant, ~line 10273-10471) plus its model dependencies in
// moddriverapi113.php/modmobileapi111extended.php. See
// models/driverAuthModel.go's doc comment for the fidelity notes on
// driver_statistics and the shift_history/driver_referral_list schema
// assumptions.
//
// Auth: unlike the legacy response (a `user_key` opaque token, persisted
// server-side in a separate user_auth_token collection - see research), this
// issues the existing tokenHelper.go session JWT (GenerateAllTokens) as
// `access_token`/`refresh_token`, matching the driver_location_history_update
// port's auth scheme. mint_test_token was a stand-in for this endpoint; once
// this ships, real logins should be used instead.
//
// Dispatched via POST /driverapi301/index?type=driver_login, same as
// check_companydomain/getcoreconfig - domain is resolved from the `Domain`
// header (not `dn`/body, since this is a real per-tenant data operation
// rather than a bootstrap call), matching driver_location_history_update's
// convention. Requires the `authkey` product header (research confirmed
// driver_login is NOT in the legacy exemption list check_companydomain/
// getcoreconfig are in) - enforced by ProductAuthenticate at the route
// group level, same as every other non-exempt dispatch type.
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
		if toString(record["password"]) != req.Password {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid password", Status: -1})
			return
		}
		if toString(record["trip_reject_block"]) == "1" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Your account is temporarily blocked due to trip rejections", Status: 10})
			return
		}
		if toString(record["status"]) != "A" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Login access denied", Status: -1})
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
		driverStatus := freeStatus
		if currentTrip, ok := profile["current_trip"].(bson.A); ok && len(currentTrip) > 0 {
			freeStatus = "B"
			driverStatus = "B"
		}

		// "Another device is already signed in" - handled unconditionally
		// based on free/busy status, not gated behind force_login (see
		// requests.DriverLoginRequest's doc comment).
		if toString(record["login_status"]) == "S" && toString(record["login_from"]) == "D" && toString(record["device_id"]) != req.DeviceID {
			if freeStatus != "F" {
				c.JSON(http.StatusOK, response.LegacyResponse{Message: "Driver is currently on a trip on another device", Status: -1})
				return
			}
			if err := models.UpdateDriverPhone(tenantDB, driverID, req.DeviceID, req.DeviceToken, req.DeviceType, "S"); err != nil {
				log.Println("driver_login: UpdateDriverPhone (takeover) error:", err)
			}
		}

		shiftStatus := "F"
		if shift, err := models.FindActiveShift(tenantDB, driverID); err != nil {
			log.Println("driver_login: FindActiveShift error:", err)
		} else if shift != nil {
			if err := models.CloseActiveShift(tenantDB, shift["_id"]); err != nil {
				log.Println("driver_login: CloseActiveShift error:", err)
			}
			shiftStatus = "OUT"
		}

		var taxiID interface{}
		if taxiID, err = models.FindTaxiForDriver(tenantDB, driverID); err != nil {
			log.Println("driver_login: FindTaxiForDriver error:", err)
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

		if err := models.UpdateDriverPhone(tenantDB, driverID, req.DeviceID, req.DeviceToken, req.DeviceType, "S"); err != nil {
			log.Println("driver_login: UpdateDriverPhone error:", err)
		}

		driverDetails := flattenDriverProfile(profile)
		driverDetails["userid"] = driverID
		driverDetails["shift_status"] = shiftStatus
		driverDetails["shiftupdate_id"] = nil
		driverDetails["taxi_id"] = taxiID
		driverDetails["driver_first_login"] = record["driver_first_login"]
		driverDetails["driver_status"] = driverStatus
		driverDetails["travel_status"] = ""
		driverDetails["driver_wallet"] = nil
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
			"shift_status":           shiftStatus,
			"time_driven":            stats.TimeDrivenToday,
			"status":                 1,
		}

		accessToken, refreshToken, err := helper.GenerateAllTokens(strconv.FormatInt(driverID, 10), domain)
		if err != nil {
			log.Println("driver_login: GenerateAllTokens error:", err)
		}

		c.JSON(http.StatusOK, gin.H{
			"message":          "You have logged in successfully",
			"status":           1,
			"detail":           gin.H{"driver_details": []bson.M{driverDetails}},
			"recent_trip_list": recentTripList,
			"sos_detail":       sosDetail,
			"access_token":     accessToken,
			"refresh_token":    refreshToken,
		})
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
