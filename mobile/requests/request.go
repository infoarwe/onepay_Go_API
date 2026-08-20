package requests

// CheckCompanyDomainRequest binds the fields moddriverapi201.php's
// check_companydomain case actually reads off $mobiledata: company_domain,
// company_main_domain, device_type ("1"=android/other, "2"=iOS, sent as a
// string). Confirmed against the Postman export for this call: it's a JSON
// sent even on a GET request - the `dt/i/pv/k/s` query
// params on the URL are unrelated client-side noise the PHP side never reads.
type CheckCompanyDomainRequest struct {
	CompanyDomain     string `json:"company_domain"`
	CompanyMainDomain string `json:"company_main_domain"`
	DeviceType        string `json:"device_type"`
}

// DriverLocationHistoryUpdateData binds the fields
// driver_location_history_update_new.js's `$location_array` reads off
// `data.data` - confirmed against a real client payload (see
// mobile/controllers/driverLocationController.go doc comment). Numeric-ish
// fields (driver_id, trip_id, distance, waiting_hour) are bound as strings
// because the client sends them as strings and the legacy handler treats
// them as such throughout (parseInt/parseFloat at point of use, never at
// bind time).
type DriverLocationHistoryUpdateData struct {
	DriverID      string  `json:"driver_id"`
	TripID        string  `json:"trip_id"`
	Locations     string  `json:"locations"`
	Status        string  `json:"status"`
	TravelStatus  string  `json:"travel_status"`
	DeviceToken   string  `json:"device_token"`
	DeviceType    string  `json:"device_type"`
	AboveMinKm    string  `json:"above_min_km"`
	Bearings      float64 `json:"bearings"`
	Distance      string  `json:"distance"`
	ShiftID       string  `json:"shift_id"`
	DriverName    string  `json:"driver_name"`
	WaitingHour   string  `json:"waiting_hour"`
	Accuracy      float64 `json:"accuracy"`
	ServiceStatus bool    `json:"service_status"`
	VersionCode   int     `json:"version_code"`
	CarrierName   string  `json:"carrier_name"`
	Brand         string  `json:"brand"`
	Model         string  `json:"model"`

	// DriverDefaultRadius mirrors $location_array['driver_default_radius'],
	// only ever seen sent by wadeenataxi per the Node source comments; left
	// blank ("") for every other tenant, matching the legacy default.
	DriverDefaultRadius string `json:"driver_default_radius"`
}

// DriverLocationHistoryUpdateRequest is the outer envelope
// (`{"data": {...}, "platform": "ANDROID", "app": "DRIVER", "lang": "en",
// "id": "6"}`), matching the curl export of the legacy Node endpoint.
type DriverLocationHistoryUpdateRequest struct {
	Data DriverLocationHistoryUpdateData `json:"data"`
	Lang string                          `json:"lang"`
}

// DriverLoginRequest binds moddriverapi201.php's driver_login case body
// (OnePayTaxi tenant, ~line 9554-9974). Password is the client's MD5 hex
// digest of the plaintext password - the legacy backend does a raw
// string-equality match against the stored `password` field, no
// server-side hashing at all - kept as-is here rather than re-hashed, for
// compatibility with existing stored driver passwords.
//
// ForceLogin IS read here (unlike BlueTaxi's driver_login, which never
// reads it): OnePayTaxi's actual case has a genuine force_login device-
// takeover branch (~line 9640) BlueTaxi's port doesn't have - see
// controllers/driverAuthController.go's DriverLogin doc comment.
type DriverLoginRequest struct {
	Phone       string `json:"phone"`
	Password    string `json:"password"`
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
	DeviceType  string `json:"device_type"`
	ForceLogin  bool   `json:"force_login"`
}

// DriverChangePasswordRequest binds moddriverapi201.php's
// driver_change_password case body. Unlike DriverLoginRequest, Password
// here is the PLAINTEXT new password - change_password()
// (moddriverapi113.php:3546-3553) does `md5($password)` server-side before
// storing, which is what driver_login later raw-compares against. DriverID
// is bound for wire compatibility with the legacy client but is not
// trusted as the target driver - see driverAuthController.go's
// DriverChangePassword doc comment.
type DriverChangePasswordRequest struct {
	DriverID        string `json:"driver_id"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

// DriverRecentTripListRequest binds moddriverapi201.php's
// driver_recent_trip_list case body. DriverID/DriverType are bound for
// wire compatibility but not trusted - see
// driverTripListController.go's doc comment.
type DriverRecentTripListRequest struct {
	DriverID    string `json:"driver_id"`
	DriverType  string `json:"driver_type"`
	DeviceToken string `json:"device_token"`
	DeviceID    string `json:"device_id"`
}

// DriverBookingListRequest binds moddriverapi201.php's driver_booking_list
// case body (OnePayTaxi tenant, ~line 10321-10333): driver_id/start/limit/
// device_type/request_type. RequestType selects which sub-list: 1 =
// driver_pending_bookings (own upcoming trip), 2 = driver_past_bookings
// (trip history), 3 = driver_show_bookings (unassigned "new show booking"
// broadcast list - the only one this Go port implements, see
// driverBookingListController.go). DriverID is bound for wire
// compatibility but not trusted as the target driver - the authenticated
// id from LegacyDriverAuthenticate (the `i` query param, validated
// against the driver's stored session token) is used instead, same
// "don't trust the body" posture as DriverRecentTripListRequest.
type DriverBookingListRequest struct {
	DriverID    string `json:"driver_id"`
	Start       int64  `json:"start"`
	Limit       int64  `json:"limit"`
	DeviceType  string `json:"device_type"`
	RequestType int    `json:"request_type"`
}
