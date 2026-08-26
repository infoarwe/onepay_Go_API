package controllers

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mobileapi/common"
	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// This file ports moddriverapi201.php action_index()'s
// 'driver_recent_trip_list' case (BlueTaxi tenant, ~line 4723-5034) - the
// driver app's home-screen dashboard payload: recent trips, today/monthly
// earnings, login/peak-hour progress against tenant-configured targets, a
// demand heatmap, notices, and the driver's own profile. See
// models/driverTripListModel.go for the full field-source mapping and
// fidelity notes.
//
// Auth: same as driver_change_password - ValidateDomain (Domain header) +
// DriverAuthenticate (userAuth JWT) resolve the tenant/driver; the body's
// driver_id/driver_type are accepted for wire compatibility but not
// trusted as authority.
//
// Deliberately NOT ported (see models/driverTripListModel.go's doc comment
// for why): the document-expiry status-41 override
// (get_expiry_dates_product), and the PACKAGE_TYPE-specific
// subscription/commission-wallet gate (status -2/-3).
func DriverRecentTripList() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.DriverRecentTripListRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request ", Status: -1})
			return
		}

		dbVal, exists := c.Get("db")
		db, ok := dbVal.(*mongo.Database)
		if !exists || !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		driverIDVal, _ := c.Get("driver_id")
		driverID := parseInt64(toStringAny(driverIDVal))
		if driverID == 0 {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request", Status: -1})
			return
		}

		driverInfo, err := models.GetDriverInfo(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverInfo error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if driverInfo == nil {
			driverInfo = bson.M{}
		}
		// The legacy "is this driver deactivated" gate ($active_status ==
		// 'D') is reproduced via people.status directly rather than
		// replicating driver_login_status()'s own DB check - session
		// validity is already established by DriverAuthenticate.
		if toString(driverInfo["status"]) == "D" {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "Your account is not yet activated, please contact your administrator.",
				Status:  10,
			})
			return
		}

		domain := c.Request.Header.Get("Domain")

		siteInfo, err := models.FindSiteInfo(db)
		if err != nil {
			log.Println("driver_recent_trip_list: FindSiteInfo error:", err)
		}
		if siteInfo == nil {
			siteInfo = bson.M{}
		}
		_, loc := resolveTenantLocation(siteInfo["user_time_zone"])
		// api_base isn't a real siteinfo field (confirmed against a live
		// dump) - the tenant's public asset host is a static deploy
		// constant in the legacy PHP, so it comes from config here instead,
		// same fallback pattern as DefaultMobileSocketBase/DefaultTimezone.
		apiBase := strings.TrimSuffix(common.Config.DefaultAssetBaseURL, "/")
		driverImageBase := apiBase + "/public/" + domain + "/driver_image/"

		now := time.Now().In(loc)
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UTC()
		dayEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC()
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).UTC()

		if err := models.ResetTodayRejectionCountIfZero(db, driverID, dayStart, dayEnd); err != nil {
			log.Println("driver_recent_trip_list: ResetTodayRejectionCountIfZero error:", err)
		}
		if req.DeviceToken != "" {
			if err := models.UpdateDriverDeviceToken(db, driverID, req.DeviceToken); err != nil {
				log.Println("driver_recent_trip_list: UpdateDriverDeviceToken error:", err)
			}
		}

		noImageURL := apiBase + "/public/common/images/no_image109.png"

		tripList, err := models.GetRecentDriverTripList(db, driverID, noImageURL, loc)
		if err != nil {
			log.Println("driver_recent_trip_list: GetRecentDriverTripList error:", err)
			tripList = []bson.M{}
		}

		driverWallet, err := models.GetDriverWallet(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverWallet error:", err)
		}

		result := gin.H{
			"message":                  "Drive Recent Trip List",
			"status":                   1,
			"trip_list":                tripList,
			"driver_threshold_setting": stringOr(siteInfo["driver_threshold_setting"], "1"),
			"driver_threshold_amount":  stringOr(siteInfo["driver_threshold_amount"], "0"),
			"accept_higher_end_model":  orEmptySlice(driverInfo["accept_higher_end_model"]),
			"driver_version_code":      subDoc(driverInfo, "driverinfo")["version_code"],
			"driver_wallet":            driverWallet,
		}
		if len(tripList) == 0 {
			result["message"] = "Your account has been activated."
			result["status"] = -1
		}

		if noticeList, err := models.GetActiveDriverNoticeList(db); err != nil {
			log.Println("driver_recent_trip_list: GetActiveDriverNoticeList error:", err)
			result["driver_notice_list"] = []bson.M{}
		} else {
			result["driver_notice_list"] = noticeList
		}

		todayEarnings, err := models.GetDriverEarnings(db, driverID, dayStart, dayEnd)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverEarnings (today) error:", err)
		}
		monthlyEarnings, err := models.GetDriverEarnings(db, driverID, monthStart, dayEnd)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverEarnings (monthly) error:", err)
		}
		totalTripCount, selfieTake, err := models.GetTodayTripInfo(db, driverID, dayStart, dayEnd)
		if err != nil {
			log.Println("driver_recent_trip_list: GetTodayTripInfo error:", err)
		}

		result["total_trips"] = totalTripCount
		result["selfie_take"] = selfieTake
		result["declined_count"] = toInt64(driverInfo["total_rejection_count"])
		result["max_declined_count"] = 3
		result["completed_trip"] = todayEarnings.TotalTrips
		result["average_rating"] = todayEarnings.AverageRating
		result["total_amount"] = formatAmount(todayEarnings.TotalAmount)
		result["total_monthly_amount"] = formatAmount(monthlyEarnings.TotalAmount)
		result["average_amount"] = monthlyEarnings.AverageAmount

		loginSummary, err := models.GetDriverLoginSummary(db, driverID, loc)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverLoginSummary error:", err)
		}
		loginHoursRequired := toFloat64Any(siteInfo["login_hours_required"])
		peakHoursRequired := toFloat64Any(siteInfo["peak_hours_required"])

		result["today_login_hours"] = loginSummary.TodayLoginHours
		result["today_peak_hours"] = loginSummary.TodayPeakHours
		result["monthly_total_login_hours"] = loginSummary.MonthlyLoginHours
		result["monthly_total_peak_hours"] = loginSummary.MonthlyPeakHours
		result["monthly_login_hour_required"] = loginHoursRequired
		result["monthly_peak_hour_required"] = peakHoursRequired

		avgPerHr := 0.0
		if loginSummary.MonthlyLoginHours > 0 {
			avgPerHr = round2(monthlyEarnings.TotalAmount / loginSummary.MonthlyLoginHours)
		}
		missedHours := loginHoursRequired - loginSummary.MonthlyLoginHours
		if missedHours < 0 {
			missedHours = 0
		}
		result["average_amount_per_hr"] = avgPerHr
		result["missed_amount"] = round2(avgPerHr * missedHours)
		result["morning_peak_hours"] = "Morning (6AM – 9AM)"
		result["evening_peak_hours"] = "Evening (5PM – 9PM)"

		heatmapThreshold := toInt64(siteInfo["driver_heatmap_seconds"])
		if heatmap, err := models.DriverHeatmap(db, heatmapThreshold); err != nil {
			log.Println("driver_recent_trip_list: DriverHeatmap error:", err)
			result["driver_heatmap"] = []bson.M{}
		} else {
			result["driver_heatmap"] = heatmap
		}

		profile, err := models.FindDriverProfile(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list: FindDriverProfile error:", err)
		}
		if profile == nil {
			profile = bson.M{}
		}
		mapping := firstTaxiMapping(profile)
		driverName := strings.TrimSpace(toString(profile["name"]) + " " + toString(profile["lastname"]))

		result["model_id"] = mapping["model_id"]
		result["model_name"] = mapping["model_name"]
		result["mapping_startdate"] = mapping["mapping_startdate"]
		result["mapping_enddate"] = mapping["mapping_enddate"]
		result["driver_code"] = profile["driver_unique_code"]
		result["driver_name"] = driverName
		result["driver_email"] = profile["email"]
		result["driver_phone"] = profile["phone"]
		result["shift_status"] = stringOr(subDoc(profile, "driverinfo")["shift_status"], "OUT")
		result["profile_picture"] = driverImageURL(driverImageBase, profile["profile_picture"])
		result["driver_licence_image"] = driverImageURL(driverImageBase, profile["driver_licence"])
		result["driver_licence_back_side_image"] = driverImageURL(driverImageBase, profile["driver_licence_back_side"])

		modelEarningMinimum, err := models.GetModelEarningMinimum(db, mapping["model_id"])
		if err != nil {
			log.Println("driver_recent_trip_list: GetModelEarningMinimum error:", err)
		}
		noLeavePenalty, err := models.GetDriverNoLeavePenaltyTotal(db, driverID, monthStart, dayEnd)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverNoLeavePenaltyTotal error:", err)
		}
		modelEarningMinimum -= noLeavePenalty
		if modelEarningMinimum < 0 {
			modelEarningMinimum = 0
		}
		result["month_earning_minimum"] = modelEarningMinimum

		remaining := 0.0
		if modelEarningMinimum > 0 {
			remaining = modelEarningMinimum - monthlyEarnings.TotalAmount
			if remaining < 0 {
				remaining = 0
			}
		}
		hoursToAchieveTarget := 0.0
		if avgPerHr > 0 && remaining > 0 {
			hoursToAchieveTarget = round2(remaining / avgPerHr)
		}
		result["hours_to_achieve_target"] = hoursToAchieveTarget

		bookingLimitConfigured, err := models.GetDriverBookingLimit(db, driverID)
		if err != nil {
			log.Println("driver_recent_trip_list: GetDriverBookingLimit error:", err)
		}
		bookedToday, err := models.CountBookingsToday(db, driverID, dayStart)
		if err != nil {
			log.Println("driver_recent_trip_list: CountBookingsToday error:", err)
		}
		result["booking_limit"] = bookingLimitConfigured - float64(bookedToday)

		c.JSON(http.StatusOK, result)
	}
}

// driverImageURL builds a full URL from a stored filename, matching the
// legacy response's shape (e.g.
// "https://uat.bluetaxiindia.com/public/uatbluetaxi/driver_image/6_driver_licence.png"),
// instead of returning the bare filename people/driver_login's response
// happens to pass through. Returns "" when there's no filename on file -
// the legacy version falls back to a "noimages.jpg" placeholder here after
// a filesystem existence check; that exact fallback filename isn't
// confirmed against live data, so this leaves it blank rather than
// guessing a name that might not exist.
func driverImageURL(base string, filename interface{}) string {
	name := toString(filename)
	if name == "" {
		return ""
	}
	return base + name
}

func stringOr(v interface{}, def string) string {
	s := toString(v)
	if s == "" {
		return def
	}
	return s
}

func orEmptySlice(v interface{}) interface{} {
	if v == nil {
		return []interface{}{}
	}
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func formatAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func toFloat64Any(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case int:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}
