package controllers

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mobileapi/common"
	"mobileapi/database"
	helper "mobileapi/helpers"
	"mobileapi/models"
	"mobileapi/requests"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
)

// CheckCompanyDomain ports moddriverapi201.php action_index's
// 'check_companydomain' case (~line 12474) plus check_company_domain() /
// update_used_status() from moddriverapi113.php.
//
// Known gaps vs. the legacy behavior, left as follow-ups rather than guessed:
//   - `encode` is always "" here. In PHP it's FLEETERA_Fleeteratokenization's
//     custom encrypt_encode(host+"-"+timestamp); that cipher hasn't been
//     ported, so faking a value would be worse than omitting it.
//   - Messages are plain English literals, not run through the site's i18n
//     layer (lang/ package not scaffolded yet).
func CheckCompanyDomain() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req requests.CheckCompanyDomainRequest
		// Body is JSON even though this is routed as GET (matches the
		// legacy client, confirmed via the Postman export).
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid Request ", Status: 2})
			return
		}

		mongoDB, ok := database.DB.(*database.MongoDB)
		if !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		// Each tenant database self-registers its own single company_domain
		// row (confirmed against live onepaytaxi/uatonepaytaxi/ridelogic
		// data) - there's no separate shared master registry, so this looks
		// up req.CompanyDomain inside the database of that same name, same
		// as database.MongoDB.IsDomainValid does. Using GetMasterDatabase()
		// here instead used to only ever resolve successfully for whichever
		// single domain happened to equal common.Config.MgDbName.
		tenantDB, err := mongoDB.GetDatabase(req.CompanyDomain)
		if err != nil {
			log.Println("check_companydomain: GetDatabase error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}

		domain, err := models.FindCompanyDomain(tenantDB, req.CompanyDomain)
		if err != nil {
			log.Println("check_companydomain: FindCompanyDomain error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if domain == nil || req.CompanyDomain == "" {
			// auth_key is "" on failure too, matching fleeteratokenization.php's
			// encrypt_encode_json (it stamps auth_key onto every
			// check_companydomain response, empty when status != 1).
			c.JSON(http.StatusOK, gin.H{"message": "sub_domain_notexists", "status": 2, "auth_key": ""})
			return
		}

		_ = models.MarkCompanyDomainUsed(tenantDB, domain.DomainID)

		companyDomain := strings.ToLower(strings.TrimSpace(req.CompanyDomain))
		protocol := "https"
		if domain.LivePort == "443" {
			protocol = "http"
		}

		host := companyDomain + "." + req.CompanyMainDomain
		baseurl := protocol + "://" + host + "/driverapi301/index/"
		baseurlMs := protocol + "://" + host + "/msdriverapi/v1/"
		updatedUrl := protocol + "://" + host + "/"

		if domain.LiveDomain != "" {
			baseurl = protocol + "://" + domain.LiveDomain + "/driverapi301/index/"
			baseurlMs = protocol + "://" + domain.LiveDomain + "/msdriverapi/v1/"
			updatedUrl = protocol + "://" + domain.LiveDomain + "/"
		}

		apiKey := domain.MobileApiKey
		if apiKey == "" {
			apiKey = common.Config.DefaultApiKey
		}

		folderPath := "public/" + companyDomain + "/android/"
		iOSImage := updatedUrl + "public/" + companyDomain + "/iOS/static_image/"
		iOSImagePath := "public/" + companyDomain + "/iOS/static_image/"
		driverColorCode := ""

		if req.DeviceType == "2" {
			folderPath = "public/" + companyDomain + "/iOS/"
			driverColorCode = updatedUrl + folderPath + "colorcode/DriverAppColor.xml"
		}
		_ = folderPath

		signInLogoDriver := "signInLogo.png"
		headerLogoDriver := "headerLogo.png"
		if _, err := os.Stat(iOSImagePath + "signInLogo_driver.png"); err == nil {
			signInLogoDriver = "signInLogo_driver.png"
		}
		if _, err := os.Stat(iOSImagePath + "headerLogo_driver.png"); err == nil {
			headerLogoDriver = "headerLogo_driver.png"
		}

		devicePaths := gin.H{
			"static_image":         iOSImage,
			"signInLogo_passenger": "signInLogo.png",
			"signInLogo_driver":    signInLogoDriver,
			"headerLogo_passenger": "headerLogo.png",
			"headerLogo_driver":    headerLogoDriver,
			"driver_language":      []string{},
			"driverColorCode":      driverColorCode,
		}

		// encode: fleeteratokenization.php's encrypt_encode() is a literal
		// passthrough (`return $encrypted;`), so the real value is just the
		// plaintext key it builds - host+"-"+unixTimestamp - not an actual
		// cipher.
		encode := host + "-" + strconv.FormatInt(time.Now().Unix(), 10)

		// auth_key: stateless JWT replacing the legacy rotating token (see
		// tokenHelper.go's GenerateAuthKey doc comment for the tradeoff).
		authKey, err := helper.GenerateAuthKey(companyDomain, "D")
		if err != nil {
			log.Println("check_companydomain: GenerateAuthKey error:", err)
			authKey = ""
		}

		message := gin.H{
			"message":          "Success!",
			"baseurl":          baseurl,
			"https_base_url":   baseurl,
			"apikey":           apiKey,
			"status":           1,
			"encode":           encode,
			"baseurl_ms":       baseurlMs,
			"default_language": "ar",
			"auth_key":         authKey,
		}
		if req.DeviceType == "2" {
			message["iOSPaths"] = devicePaths
		} else {
			message["androidPaths"] = devicePaths
		}

		c.JSON(http.StatusOK, message)
	}
}

// topLevelSiteInfoKeys are pulled out of `detail` and placed at the top
// level of the response instead, matching where moddriverapi201.php's
// getcoreconfig case actually puts them (most fields land in
// $config_array[0] -> "detail", these few are assigned directly onto
// $json_decode / $message instead).
var topLevelSiteInfoKeys = []string{
	"reconnect_socket", "driver_wallet_enable", "driver_settlement_enable",
	"street_pickup_enable", "dispatcher_phone_number", "iOSStaticImage",
	"driver_app_help_url", "timezone", "activity_bg", "error_logs",
}

// GetCoreConfig ports moddriverapi201.php action_index's 'getcoreconfig'
// case (line 220-686 for the BlueTaxi tenant), reading live from the same
// collections the legacy PHP/admin panel does instead of a manually-seeded
// snapshot (see models/siteInfoModel.go's doc comment for the full
// field-source research this was ported against, and why some of the
// smaller list collections are read as a plain Find rather than a
// byte-exact replica of their legacy aggregation pipelines).
//
// Deliberate deviations from the legacy behavior, since this Go service is
// one process serving every tenant rather than a per-tenant PHP deploy:
//   - timezone/mobile_socket_http_url base come from the tenant's siteinfo
//     document if present, else common.Config.DefaultTimezone/
//     DefaultMobileSocketBase - the legacy version reads these from a
//     per-tenant static Setting.php constant, which has no Go equivalent.
//   - current_time/utc_time are computed via time.LoadLocation(timezone)
//     instead of a stored tz_offset_seconds field, so they track real DST
//     rules instead of a value someone has to keep updated by hand.
//   - language_color's URLs come from siteinfo's own language_color_base
//     field if present, else a best-effort default built from api_base +
//     domain - the legacy version scans the PHP app's upload directory on
//     disk, which this Go service has no access to.
//   - auth_key has no legacy equivalent in this dispatch case at all; it's
//     a deliberate addition for this Go rewrite's driver auth flow.
//
// Tenant is resolved via the `dn` query param (confirmed against the
// Postman export - this call doesn't carry company_domain/company_main_domain
// in a body the way check_companydomain does).
func GetCoreConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Query("dn")

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

		raw, err := models.FindSiteInfo(tenantDB)
		if err != nil {
			log.Println("getcoreconfig: FindSiteInfo error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			return
		}
		if raw == nil {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "siteinfo not configured for this domain",
				Status:  -1,
			})
			return
		}

		detail := flattenSiteInfo(raw)

		// vehicle_state_list/vehicle_info_list/vehicle_color_list/
		// vehicle_plate_prefix_list/model_details/gateway_array/
		// passenger_payment_option: each a separate live collection read
		// (see siteInfoModel.go). Logged and left empty on error rather
		// than failing the whole response - a config call degrading one
		// list is better than a driver/passenger app getting nothing.
		if v, err := models.FindVehicleStateList(tenantDB); err != nil {
			log.Println("getcoreconfig: FindVehicleStateList error:", err)
		} else {
			detail["vehicle_state_list"] = v
		}
		if v, err := models.FindVehicleInfoList(tenantDB); err != nil {
			log.Println("getcoreconfig: FindVehicleInfoList error:", err)
		} else {
			detail["vehicle_info_list"] = v
		}
		if v, err := models.FindVehicleColorList(tenantDB); err != nil {
			log.Println("getcoreconfig: FindVehicleColorList error:", err)
		} else {
			detail["vehicle_color_list"] = v
		}
		if v, err := models.FindVehiclePlatePrefixList(tenantDB); err != nil {
			log.Println("getcoreconfig: FindVehiclePlatePrefixList error:", err)
		} else {
			detail["vehicle_plate_prefix_list"] = v
		}
		if v, err := models.FindModelDetails(tenantDB); err != nil {
			log.Println("getcoreconfig: FindModelDetails error:", err)
		} else {
			detail["model_details"] = v
		}
		if v, err := models.FindGatewayDetails(tenantDB); err != nil {
			log.Println("getcoreconfig: FindGatewayDetails error:", err)
		} else {
			// Legacy getcoreconfig uses the same query result for both
			// fields (moddriverapi201.php:469-484).
			detail["gateway_array"] = v
			detail["passenger_payment_option"] = v
		}

		if currency, err := models.FindDefaultCurrency(tenantDB); err != nil {
			log.Println("getcoreconfig: FindDefaultCurrency error:", err)
		} else if currency != nil {
			if v, ok := currency["telephone_code"]; ok {
				detail["country_code"] = v
			}
			if v, ok := currency["iso_country_code"]; ok {
				detail["country_iso_code"] = v
			}
			if v, ok := currency["currency_symbol"]; ok {
				detail["site_currency"] = v
			}
		}

		if gateway, err := models.FindDefaultPaymentGateway(tenantDB); err != nil {
			log.Println("getcoreconfig: FindDefaultPaymentGateway error:", err)
		} else if gateway != nil {
			detail["default_payment_id"] = gateway["_id"]
		}

		timezone, loc := resolveTenantLocation(detail["timezone"])
		nowUTC := time.Now().UTC()
		_, offsetSeconds := nowUTC.In(loc).Zone()
		utcTime := nowUTC.Unix()
		currentTime := utcTime + int64(offsetSeconds)
		gtLstTime := currentTime
		detail["timezone"] = timezone
		detail["utc_time"] = utcTime
		detail["current_time"] = currentTime

		mobileSocketBase, _ := detail["mobile_socket_base"].(string)
		if mobileSocketBase == "" {
			mobileSocketBase = common.Config.DefaultMobileSocketBase
		}
		socketURL := mobileSocketBase + "?origin=" + domain

		if s, _ := detail["driver_app_help_url"].(string); s == "" {
			detail["driver_app_help_url"] = "http://mongo.fleetera.io/apphelp.php"
		}
		if detail["iOSStaticImage"] == nil {
			detail["iOSStaticImage"] = utcTime
		}

		languageColorStatus := bson.M{
			"ios_driver_language":         raw["ios_driver_language"],
			"ios_passenger_language":      raw["ios_passenger_language"],
			"ios_driver_colorcode":        raw["ios_driver_colorcode"],
			"ios_passenger_colorcode":     raw["ios_passenger_colorcode"],
			"android_driver_language":     raw["android_driver_language"],
			"android_passenger_language":  raw["android_passenger_language"],
			"android_passenger_colorcode": raw["android_passenger_colorcode"],
			"android_driver_colorcode":    raw["android_driver_colorcode"],
		}

		langColorBase, ok := raw["language_color_base"].(bson.M)
		if !ok {
			apiBase, _ := detail["api_base"].(string)
			langColorBase = defaultLanguageColorBase(apiBase, domain)
		}
		languageColor := buildLanguageColor(langColorBase, currentTime)

		authKey, err := helper.GenerateAuthKey(domain, "D")
		if err != nil {
			log.Println("getcoreconfig: GenerateAuthKey error:", err)
			authKey = ""
		}

		extracted := bson.M{}
		for _, k := range topLevelSiteInfoKeys {
			extracted[k] = detail[k]
			delete(detail, k)
		}

		message := gin.H{
			"message":                   "Success!",
			"detail":                    []bson.M{detail},
			"status":                    1,
			"language_color_status":     languageColorStatus,
			"language_color":            languageColor,
			"reconnect_socket":          extracted["reconnect_socket"],
			"driver_wallet_enable":      extracted["driver_wallet_enable"],
			"driver_settlement_enable":  extracted["driver_settlement_enable"],
			"street_pickup_enable":      extracted["street_pickup_enable"],
			"dispatcher_phone_number":   extracted["dispatcher_phone_number"],
			"mobile_socket_http_url":    socketURL,
			"mobile_socket_http_domain": domain,
			"https_node_url":            socketURL,
			"iOSStaticImage":            extracted["iOSStaticImage"],
			"driver_app_help_url":       extracted["driver_app_help_url"],
			"timezone":                  extracted["timezone"],
			"activity_bg":               extracted["activity_bg"],
			"error_logs":                extracted["error_logs"],
			"gt_lst_time":               gtLstTime,
			"auth_key":                  authKey,
		}

		c.JSON(http.StatusOK, message)
	}
}

// flattenSiteInfo merges FindSiteInfo's joined themesettings/mapsettings
// sub-documents onto the top level (matching how the legacy PHP's
// $lookup+$unwind result gets folded into one $config_array[0]), and pulls
// the map-settings flags into a nested "map_settings" object matching the
// getcoreconfig response's actual shape. android_google_key is renamed to
// android_google_api_key (the field name confirmed via
// mobile_common_config.php:521's ANDROID_GOOGLE_GEO_API_KEY constant).
func flattenSiteInfo(raw bson.M) bson.M {
	detail := bson.M{}
	for k, v := range raw {
		if k == "_id" || k == "themesettings" || k == "mapsettings" {
			continue
		}
		detail[k] = v
	}
	if theme, ok := raw["themesettings"].(bson.M); ok {
		for k, v := range theme {
			if k != "_id" {
				detail[k] = v
			}
		}
	}
	mapSettings := bson.M{}
	if ms, ok := raw["mapsettings"].(bson.M); ok {
		for k, v := range ms {
			if k != "_id" {
				detail[k] = v
			}
		}
		mapSettings = bson.M{
			"display_current_location": ms["display_current_location"],
			"enable_route":             ms["enable_route"],
			"is_google_distance":       ms["is_google_distance"],
			"is_google_direction":      ms["is_google_direction"],
			"is_google_geocode":        ms["is_google_geocode"],
		}
	}
	detail["map_settings"] = mapSettings

	if _, hasNew := detail["android_google_api_key"]; !hasNew {
		if v, ok := detail["android_google_key"]; ok {
			detail["android_google_api_key"] = v
		}
	}
	return detail
}

// defaultLanguageColorBase builds language_color URLs from the tenant's own
// api_base + a conventional public/<domain>/<platform>/colorcode/ path,
// used when siteinfo has no explicit language_color_base field. The legacy
// PHP builds these by scanning a filesystem directory this Go service has
// no access to, so this is a best-effort stand-in, not a byte-exact port.
func defaultLanguageColorBase(apiBase, domain string) bson.M {
	base := strings.TrimSuffix(apiBase, "/") + "/public/" + domain
	return bson.M{
		"android": bson.M{
			"driver_language":    []string{},
			"passenger_language": []string{},
			"colorcode":          base + "/android/colorcode/passengerAppColors.xml",
			"driverColorCode":    base + "/android/colorcode/driverAppColors.xml",
		},
		"iOS": bson.M{
			"driver_language":    []string{},
			"passenger_language": []string{},
			"colorcode":          base + "/iOS/colorcode/PassengerAppColor.xml",
			"driverColorCode":    base + "/iOS/colorcode/DriverAppColor.xml",
		},
	}
}

// resolveTenantLocation resolves a tenant's timezone name (as read off a
// siteinfo document, may be missing/empty) to a *time.Location, falling
// back to common.Config.DefaultTimezone and then "UTC". Shared by
// GetCoreConfig (current_time/utc_time) and DriverLogin (today's day
// bounds for driver_statistics).
func resolveTenantLocation(rawTimezone interface{}) (string, *time.Location) {
	timezone, _ := rawTimezone.(string)
	if timezone == "" {
		timezone = common.Config.DefaultTimezone
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		log.Println("resolveTenantLocation: unknown timezone", timezone, "- falling back to UTC:", err)
		loc = time.UTC
	}
	return timezone, loc
}

func toInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int32:
		return int64(val)
	case int:
		return int64(val)
	case float64:
		return int64(val)
	default:
		return 0
	}
}

// buildLanguageColor appends a cache-busting timeCache param onto the
// stored colorcode/driverColorCode URLs, matching the legacy response's
// "...xml?timeCache=<unix_ts>" shape.
func buildLanguageColor(base bson.M, timeCache int64) bson.M {
	out := bson.M{}
	for platform, v := range base {
		platformMap, ok := v.(bson.M)
		if !ok {
			continue
		}
		entry := bson.M{}
		for k, val := range platformMap {
			if s, isStr := val.(string); isStr && (k == "colorcode" || k == "driverColorCode") {
				entry[k] = s + "?timeCache=" + strconv.FormatInt(timeCache, 10)
			} else {
				entry[k] = val
			}
		}
		out[platform] = entry
	}
	return out
}
