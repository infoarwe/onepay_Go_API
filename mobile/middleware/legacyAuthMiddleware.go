package middleware

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"mobileapi/common"
	"mobileapi/models"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"
)

// LegacyProductAuthenticate ports the product-key check at the top of
// fleetera_trial_mobilekey_tokenization.php (OnePayTaxi tenant): the
// mobile client sends the fixed per-product key in the `Authorization`
// header (NOT `authkey` - that's BlueTaxi's distinct scheme, see
// authMiddleware.go's ProductAuthenticate). Unlike ProductAuthenticate,
// the legacy PHP hard-codes this value in source rather than reading it
// per-company from the DB; it's sourced from config here
// (common.Config.LegacyAuthorizationKey) instead of being duplicated as a
// literal, but the check itself (== a single fixed value, not a
// per-tenant lookup) is unchanged.
func LegacyProductAuthenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		authToken := strings.TrimSpace(c.Request.Header.Get("Authorization"))
		if authToken == "" || authToken != common.Config.LegacyAuthorizationKey {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "api key not given",
				Status:  -8,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// LegacyDriverAuthenticate ports the `userAuth` header check inside
// fleeteratokenization.php's encrypt_encode_json() (OnePayTaxi tenant) -
// the legacy PHP validates it against MDB_USER_TOKEN scoped to the driver
// id sent as the `i` query-string param (see
// models.AuthenticateUserToken's doc comment for why `i` rather than the
// JSON body's driver_id is the trusted identity). Requires ValidateDomain
// to have already run (for `db` in context) - unlike DriverAuthenticate's
// JWT, this is a DB lookup so it needs the tenant database resolved
// first, same requirement as the rest of the per-tenant middleware chain.
//
// Deliberately a separate middleware from LegacyProductAuthenticate: the
// legacy PHP runs the product-key check unconditionally but only requires
// a valid `userAuth` for methods outside its own exemption list
// (driver_login, signup, otp, etc. - see fleeteratokenization.php:50).
// driver_booking_list is not in that exemption list, so any route using
// this middleware is implicitly one that requires a valid session, same
// as the legacy per-method gate.
func LegacyDriverAuthenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		dbVal, exists := c.Get("db")
		db, ok := dbVal.(*mongo.Database)
		if !exists || !ok {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			c.Abort()
			return
		}

		userAuth := c.Request.Header.Get("userAuth")
		driverID, _ := strconv.ParseInt(c.Query("i"), 10, 64)

		if userAuth == "" {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "User token not exists", Status: 128})
			c.Abort()
			return
		}
		if driverID == 0 {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid user token", Status: 128})
			c.Abort()
			return
		}

		tok, err := models.AuthenticateUserToken(db, userAuth, driverID)
		if err != nil {
			log.Println("LegacyDriverAuthenticate: AuthenticateUserToken error:", err)
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Database Connection Failed", Status: 2})
			c.Abort()
			return
		}
		if tok == nil {
			c.JSON(http.StatusOK, response.LegacyResponse{Message: "Invalid user token", Status: 128})
			c.Abort()
			return
		}

		c.Set("driver_id", tok.DriverID)
		c.Next()
	}
}
