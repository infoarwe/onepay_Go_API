package routes

import (
	"github.com/gin-gonic/gin"
)

func RegisterAllRoutes(router *gin.Engine) {
	RegisterBootstrapRoutes(router)
	RegisterDriverLocationRoutes(router)
	RegisterDriverAuthRoutes(router)
	RegisterDriverTripListRoutes(router)
	RegisterDriverBookingListRoutes(router)
}
