package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
	_ "time/tzdata" // embeds the IANA tz database, so time.LoadLocation (used by getcoreconfig's timezone offset) works even on hosts without OS-level tzdata

	"mobileapi/common"
	"mobileapi/database"
	routes "mobileapi/routes"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func initConfig() {
	os.Setenv("TZ", "UTC")

	if err := common.LoadConfig(); err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	mongoDB := &database.MongoDB{}
	if err := mongoDB.Init(); err != nil {
		log.Fatalf("Error initializing MongoDB: %v", err)
	}

	database.DB = mongoDB
	mongoDB.LoadDomains()
}

func setupRouter() *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	gin.SetMode(common.Config.Mode)

	corsConfig := cors.Config{
		AllowOrigins:     []string{common.Config.CorsOrigins},
		AllowMethods:     []string{"GET", "POST"},
		AllowHeaders:     []string{"authkey", "userAuth", "Content-Type", "Domain", "Accept-Language"},
		AllowCredentials: true,
	}
	router.Use(cors.New(corsConfig))

	routes.RegisterAllRoutes(router)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	return router
}

func startServer(router *gin.Engine) {
	port := fmt.Sprint(common.Config.Port)
	server := &http.Server{
		Addr:           port,
		Handler:        router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    30 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		log.Printf("Server listening on %s\n", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited gracefully")
}

func main() {
	initConfig()
	defer database.DB.Close()

	router := setupRouter()
	startServer(router)
}
