package routes

import (
	controller "mobileapi/controllers"
	"mobileapi/middleware"

	"github.com/gin-gonic/gin"
)

// RegisterBootstrapRoutes registers check_companydomain and getcoreconfig
// as dedicated top-level routes (moved off /driverapi301/index?type=... -
// the whole type-dispatched /index endpoint has been retired now that every
// dispatch type has its own route). Neither uses ProductAuthenticate: both
// are exempt from the `authkey` product-key check in the legacy PHP, since
// they run before a company/domain (and therefore a product key) is known.
func RegisterBootstrapRoutes(router *gin.Engine) {
	router.POST("/check_companydomain", controller.CheckCompanyDomain())
	router.POST("/getcoreconfig", controller.GetCoreConfig())
}

// RegisterDriverLocationRoutes registers
// POST /driver_location_history_update at the top level (not nested under
// /driverapi301, matching the legacy Node service's route table - it's a
// dedicated route there, not one of the type-dispatched /index cases).
// Requires a resolved tenant (ValidateDomain, via the Domain header) and an
// authenticated driver session (DriverAuthenticate, via userAuth) - see
// driverLocationController.go's doc comment for why this uses the existing
// session JWT rather than the legacy device-token scheme.
func RegisterDriverLocationRoutes(router *gin.Engine) {
	router.POST("/driver_location_history_update",
		middleware.ValidateDomain(),
		middleware.DriverAuthenticate(),
		controller.DriverLocationHistoryUpdate(),
	)
}

// RegisterDriverAuthRoutes registers POST /driver_login at the top level,
// same pattern as RegisterDriverLocationRoutes - a dedicated path instead
// of going through /driverapi301/index?type=driver_login, even though the
// legacy PHP dispatches it as one of action_index()'s switch cases.
// ProductAuthenticate is still required (the `authkey` header) - research
// confirmed driver_login is not in the legacy exemption list
// check_companydomain/getcoreconfig are in. There's no DriverAuthenticate
// here since a driver isn't authenticated yet at login time; domain
// resolution happens inside DriverLogin() itself (via the Domain header),
// same self-contained style as GetCoreConfig.
func RegisterDriverAuthRoutes(router *gin.Engine) {
	router.POST("/driver_login",
		middleware.ProductAuthenticate(),
		controller.DriverLogin(),
	)

	// driver_change_password is a logged-in action, unlike driver_login -
	// resolves the tenant via ValidateDomain and the driver via
	// DriverAuthenticate's session JWT, same pattern as
	// driver_location_history_update. See DriverChangePassword's doc
	// comment for why the body's driver_id isn't trusted as the target.
	router.POST("/driver_change_password",
		middleware.ValidateDomain(),
		middleware.DriverAuthenticate(),
		controller.DriverChangePassword(),
	)
}

// RegisterDriverTripListRoutes registers POST /driver_recent_trip_list -
// same auth pattern as driver_change_password (ValidateDomain +
// DriverAuthenticate), since it's a logged-in driver's dashboard payload.
func RegisterDriverTripListRoutes(router *gin.Engine) {
	router.POST("/driver_recent_trip_list",
		middleware.ValidateDomain(),
		middleware.DriverAuthenticate(),
		controller.DriverRecentTripList(),
	)
}

// RegisterDriverBookingListRoutes registers POST /driver_booking_list for
// the OnePayTaxi tenant - deliberately a DIFFERENT auth chain from every
// other route in this file: LegacyProductAuthenticate (the fixed
// `Authorization` header, not `authkey`) + ValidateDomain +
// LegacyDriverAuthenticate (the DB-backed opaque `userAuth` token, not the
// JWT DriverAuthenticate expects) - see driverBookingListController.go and
// middleware/legacyAuthMiddleware.go's doc comments for why this tenant
// needs its own scheme instead of reusing the BlueTaxi auth chain above.
func RegisterDriverBookingListRoutes(router *gin.Engine) {
	router.POST("/driver_booking_list",
		middleware.LegacyProductAuthenticate(),
		middleware.ValidateDomain(),
		middleware.LegacyDriverAuthenticate(),
		controller.DriverBookingList(),
	)
}
