package middleware

import (
	"net/http"
	"strings"

	"mobileapi/common"
	helper "mobileapi/helpers"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
)

// ProductAuthenticate checks the legacy `authkey` header (confirmed via the
// Postman export - not `Authorization`) against the configured product key.
// Only attach this to routes that actually require it - check_companydomain
// and getcoreconfig are exempt in the legacy PHP (they run before a
// company/domain is known), so their routes simply don't use this
// middleware at all rather than this middleware special-casing them.
func ProductAuthenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		productToken := strings.TrimSpace(c.Request.Header.Get("authkey"))
		if productToken == "" || productToken != common.Config.ProductKey {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "Invalid or missing Product Authorization Token",
				Status:  -8,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// DriverAuthenticate validates the driver JWT (sent via the `userAuth`
// header, confirmed via the Postman export) and injects driver_id/
// company_domain into the context. Not used by the bootstrap dispatch types.
func DriverAuthenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientToken := c.Request.Header.Get("userAuth")
		if clientToken == "" {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: "Driver Authorization Token Missing",
				Status:  -1,
			})
			c.Abort()
			return
		}

		claims, errMsg := helper.ValidateAccessToken(clientToken)
		if errMsg != "" {
			c.JSON(http.StatusOK, response.LegacyResponse{
				Message: errMsg,
				Status:  -1,
			})
			c.Abort()
			return
		}

		c.Set("driver_id", claims.DriverID)
		c.Set("company_domain", claims.CompanyDomain)
		c.Next()
	}
}
