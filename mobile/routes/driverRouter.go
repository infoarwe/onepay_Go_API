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

// RegisterDriverAuthRoutes registers POST /driver_login at the top level -
// a dedicated path instead of going through
// /driverapi201/index?type=driver_login, even though the legacy PHP
// dispatches it as one of action_index()'s switch cases. Uses
// LegacyProductAuthenticate (the fixed `Authorization` header) rather than
// ProductAuthenticate/`authkey` - OnePayTaxi's distinct scheme, see
// middleware/legacyAuthMiddleware.go and driverAuthController.go's
// DriverLogin doc comment. There's no driver-session middleware here since
// a driver isn't authenticated yet at login time; domain resolution
// happens inside DriverLogin() itself (via the Domain header), same
// self-contained style as GetCoreConfig.
func RegisterDriverAuthRoutes(router *gin.Engine) {
	router.POST("/driver_login",
		middleware.LegacyProductAuthenticate(),
		controller.DriverLogin(),
	)

	// driver_change_password still uses BlueTaxi's JWT auth chain
	// (ValidateDomain + DriverAuthenticate) inherited from the original
	// port - NOT yet switched to LegacyProductAuthenticate/
	// LegacyDriverAuthenticate like driver_login/driver_booking_list
	// above, so it will reject OnePayTaxi's actual mobile app as-is. Out
	// of scope for this pass; needs the same auth-chain fix before it's
	// usable for this tenant.
	router.POST("/driver_change_password",
		middleware.ValidateDomain(),
		middleware.DriverAuthenticate(),
		controller.DriverChangePassword(),
	)
}

// RegisterDriverTripListRoutes registers POST /driver_recent_trip_list,
// shared by both tenants at the same path - each sends its product-auth
// token under a different header name (confirmed via the two Postman
// exports), which is what driverTripListDispatch() below switches on:
// BlueTaxi sends `authkey` (ProductAuthenticate + DriverAuthenticate's JWT
// `userAuth`); OnePayTaxi sends `Authorization` (LegacyProductAuthenticate +
// LegacyDriverAuthenticate's DB-backed opaque `userAuth`). Each chain leads
// to its own controller - controller.DriverRecentTripList() ports
// BlueTaxi's case of this name, controller.DriverRecentTripListLegacy()
// ports OnePayTaxi's (a functionally different, much simpler response
// shape - see that file's doc comment).
func RegisterDriverTripListRoutes(router *gin.Engine) {
	router.POST("/driver_recent_trip_list", driverTripListDispatch())
}

// driverTripListDispatch hand-runs one of two full middleware+handler
// chains against the same *gin.Context, rather than registering two routes
// for one path (gin doesn't allow that). Calling a gin.HandlerFunc directly
// like this is safe: each middleware's own internal c.Next() just becomes a
// no-op (there's nothing left in gin's real registered chain for this route
// besides this dispatcher), and c.Abort() only sets a flag on the shared
// Context - checked here via c.IsAborted() between steps - so a failed
// auth/domain check still stops the chain and its JSON error response
// still wins.
func driverTripListDispatch() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			middleware.LegacyProductAuthenticate()(c)
			if c.IsAborted() {
				return
			}
			middleware.ValidateDomain()(c)
			if c.IsAborted() {
				return
			}
			middleware.LegacyDriverAuthenticate()(c)
			if c.IsAborted() {
				return
			}
			controller.DriverRecentTripListLegacy()(c)
			return
		}

		middleware.ProductAuthenticate()(c)
		if c.IsAborted() {
			return
		}
		middleware.ValidateDomain()(c)
		if c.IsAborted() {
			return
		}
		middleware.DriverAuthenticate()(c)
		if c.IsAborted() {
			return
		}
		controller.DriverRecentTripList()(c)
	}
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
