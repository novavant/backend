package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"project/database"
	"project/middleware"
	"project/models"
	"project/routes"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env if present (do not overwrite already-set environment variables).
	// Always attempt to load so DB_HOST, DB_NAME, etc are available when running locally.
	if envMap, err := godotenv.Read(); err == nil {
		for k, v := range envMap {
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}

	// Validate required environment variables
	requiredEnvVars := []string{"DB_HOST", "DB_USER", "DB_PASS", "DB_NAME", "JWT_SECRET"}
	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			log.Fatalf("Required environment variable %s is not set", envVar)
		}
	}

	// Connect to the database
	db, err := database.Connect()
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Auto-migrate only in development to avoid accidental production schema changes
	if strings.ToLower(os.Getenv("ENV")) == "development" {
		log.Println("Running in development mode - performing auto-migration")
		if err := db.AutoMigrate(
			&models.Admin{},
			&models.RefreshToken{},
			&models.User{},
			&models.Category{},
			&models.Product{},
			&models.Investment{},
			&models.UserSpin{},
			&models.Setting{ClosedRegister: false, Maintenance: false},
			&models.Payment{},
			&models.PaymentSettings{},
			&models.ChatSession{},
			&models.ChatMessage{},
			&models.TransferContact{},
			&models.Gift{},
			&models.GiftAmountSlot{},
			&models.GiftClaim{},
		); err != nil {
			log.Fatalf("failed to migrate database: %v", err)
		}
		log.Println("Auto-migration completed successfully")
	} else {
		log.Println("Running in production mode - skipping auto-migration")
		// Ensure critical columns exist (add profile to users if missing)
		if err := db.Exec("ALTER TABLE users ADD COLUMN profile VARCHAR(255) NULL").Error; err != nil {
			if !strings.Contains(err.Error(), "Duplicate column") {
				log.Printf("[warn] schema migration (profile): %v", err)
			}
		}
	}

	// Initialize router
	router := routes.InitRouter()

	// Wrap router with global middleware in recommended order
	// Logging -> Security headers / CORS -> Request ID -> Max Body -> Timeout -> Recovery -> Metrics -> Suspicious Activity
	handler := middleware.RequestLogMiddleware(
		middleware.SecurityHeadersMiddleware(
			middleware.RequestIDMiddleware(
				middleware.MaxBodyMiddleware(
					middleware.TimeoutMiddleware(
						middleware.RecoveryMiddleware(
							middleware.MetricsMiddleware(
								middleware.SuspiciousActivityMiddleware(router),
							),
						),
					),
				),
			),
		),
	)

	// Create HTTP server with production-ready configuration
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	server := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Server starting on port %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Give outstanding requests 30 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
