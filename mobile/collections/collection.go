/** Set Constant variable for Collection ***/

package collections

const (
	DRIVER    = "driver"    // store all driver details
	PASSENGER = "passenger" // store all passenger details

	// COMPANY_DOMAIN lives in the master database. It is the tenant-bootstrap
	// lookup table the legacy PHP check_companydomain call reads from
	// (modconnection/classes/model/moddriverapi113.php:check_company_domain).
	// Confirmed fields: company_domain, _id, DBtype, live_domain,
	// mobile_api_key, live_port, db_exists, used_status, used_date.
	COMPANY_DOMAIN = "company_domain"

	// Collections behind getcoreconfig (models/siteInfoModel.go), confirmed
	// against the legacy PHP source for the BlueTaxi tenant
	// (moddriverapi201.php's getcoreconfig case, modmobileapi111extended.php's
	// select_site_settings()/company_model_details(), fleeteracommonmodel.php's
	// common_site_info()/gateway_details()/common_currency_details()). These
	// are read live on every request - there is deliberately no local
	// snapshot/cache collection, since the whole point is to reflect whatever
	// the admin panel currently has saved without needing a redeploy or a
	// manual reseed step.
	SITEINFO             = "siteinfo"
	THEME_SETTINGS       = "theme_settings"
	MAP_SETTINGS         = "map_settings"
	CSC                  = "csc" // country/state/city
	VEHICLE_INFO         = "vehicle_info"
	VEHICLE_COLOR        = "vehicle_color"
	VEHICLE_PLATE_PREFIX = "vehicle_plate_prefix"
	PAYMENT_MODULES      = "payment_modules"
	PAYMENT_GATEWAYS     = "payments_gateways"

	// Per-tenant collections used by the driver_location_history_update port
	// (models/driverLocationModel.go). Names confirmed against the legacy
	// Node service's common/table_config.json (MDB_PEOPLE, MDB_PASSENGERS_LOGS,
	// MDB_PASSENGERS, MDB_TAXI, MDB_MOTOR_MODEL, MDB_COMPANY,
	// MDB_SCHEDULE_TRIPS, MDB_TAXI_DRIVER_MAPPING) - these are the real,
	// already-populated collections in each tenant database, not new schema.
	PEOPLE              = "people"
	PASSENGERS_LOGS     = "passengers_logs"
	PASSENGERS          = "passengers"
	TAXI                = "taxi"
	MOTOR_MODEL         = "motor_model"
	COMPANY             = "company"
	SCHEDULE_TRIPS      = "schedule_trips"
	TAXI_DRIVER_MAPPING = "taxi_driver_mapping"

	// Collections behind driver_login (models/driverAuthModel.go). Names
	// confirmed against the legacy PHP source's
	// application/classes/table_config.php (MDB_DRIVER_REF,
	// MDB_REJECTION_HISTORY, MDB_SHIFT_HISTORY constants).
	DRIVER_REF        = "driver_referral_list"
	REJECTION_HISTORY = "driver_rejection_list"
	SHIFT_HISTORY     = "driver_shift_history"

	// Collections behind driver_recent_trip_list
	// (models/driverTripListModel.go), confirmed against
	// application/classes/table_config.php's MDB_DRIVER_SELFIES,
	// MDB_DRIVER_DAILY_LOGIN, MDB_DRIVER_NOTICE,
	// MDB_DRIVER_NO_LEAVE_PENALITY constants.
	DRIVER_SELFIE           = "driver_selfie"
	DRIVER_DAILY_LOGIN      = "driver_daily_logins"
	DRIVER_NOTICE           = "driver_notice"
	DRIVER_NO_LEAVE_PENALTY = "driver_no_leave_penality"

	// USER_TOKEN backs the legacy opaque-session-token auth scheme (OnePayTaxi
	// tenant): moddriverapi113.php's valid_userAuth()/manage_userKey()
	// (modmobileapi111extended.php) read/write this collection by
	// `new_user_key` + `driver_id`/`passenger_id`. Confirmed against
	// application/classes/table_config.php: MDB_USER_TOKEN = TABLE_PREFIX .
	// "user_auth_token" (empty prefix). Distinct from BlueTaxi's JWT
	// (helpers/tokenHelper.go) - this tenant's mobile app was never updated
	// to send a `userAuth` JWT, it still sends the DB-backed opaque token
	// issued at driver_login time, so driver_booking_list's auth (see
	// middleware/legacyAuthMiddleware.go) validates against this collection
	// directly instead of requiring driver_login to move to Go first.
	USER_TOKEN = "user_auth_token"
)
