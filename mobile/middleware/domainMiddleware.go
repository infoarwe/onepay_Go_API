package middleware

import (
	"net/http"

	"mobileapi/database"
	"mobileapi/response"

	"github.com/gin-gonic/gin"
)

// ValidateDomain resolves the per-tenant database for routes that already
// know which company_domain they belong to (everything after the mobile app
// has called check_companydomain and stored a Domain to send back).
func ValidateDomain() gin.HandlerFunc {
	return func(c *gin.Context) {
		domain := c.Request.Header.Get("Domain")
		if domain == "" {
			c.JSON(http.StatusBadRequest, response.LegacyResponse{
				Message: "Domain Header Missing",
				Status:  -1,
			})
			c.Abort()
			return
		}

		mongoDB, ok := database.DB.(*database.MongoDB)
		if !ok || !mongoDB.IsDomainValid(domain) {
			c.JSON(http.StatusUnauthorized, response.LegacyResponse{
				Message: "Invalid Domain",
				Status:  -1,
			})
			c.Abort()
			return
		}

		db, _ := mongoDB.GetDatabase(domain)
		c.Set("db", db)
		c.Set("domain", domain)

		c.Next()
	}
}
