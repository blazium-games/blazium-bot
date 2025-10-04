package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/servusdei2018/shards/v2"
)

type config struct {
	Token string
}

var (
	cfg config
	Mgr *shards.Manager
)

func init() {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		fmt.Printf("Error loading .env file: %v\n", err)
	}

	// Initialize the logger first
	initLogger()

	// Get the BOT_TOKEN from the environment
	cfg = config{
		Token: os.Getenv("DISCORD_TOKEN"),
	}

	if cfg.Token == "" {
		appLogger.Error("DISCORD_TOKEN is required but not set in the environment")
		os.Exit(1)
	}

	appLogger.Info("Configuration loaded successfully")
}

func main() {
	appLogger.Info("Starting Blazium Bot...")

	// Create a new router using Gorilla Mux
	r := mux.NewRouter()

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://blazium.app", http.StatusMovedPermanently)
	})

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Set the content type to application/json
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Define a health check response structure
		response := map[string]string{"status": "healthy"}

		// Encode the response as JSON and send it
		json.NewEncoder(w).Encode(response)
	})

	embedHandler := embedMiddleware(r)
	corsHandler := enableCORS(embedHandler)

	// Start the Discord bot system
	runDiscordBot()

	// Set up signal handling for graceful shutdown
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)

	// Start HTTP server in a goroutine
	go func() {
		appLogger.Info("Starting HTTP server on :8080")
		err := http.ListenAndServe(":8080", corsHandler)
		if err != nil {
			appLogger.Errorf("Error starting HTTP server: %v", err)
		}
	}()

	// Wait for shutdown signal
	appLogger.Info("Bot is now running. Press CTRL-C to exit.")
	<-sc

	// Gracefully shutdown
	appLogger.Info("Shutdown signal received. Stopping bot...")
	if CommandManager != nil {
		appLogger.Info("Cleaning up commands and shutting down...")
		if err := CommandManager.StopShards(); err != nil {
			appLogger.Errorf("Error stopping shard manager: %v", err)
		} else {
			appLogger.Info("Bot shutdown completed successfully")
		}
	}
	appLogger.Info("Bot shutdown complete.")
}

func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*") // Allow all origins, you can restrict this to a specific domain
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Handle preflight OPTIONS requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Call the next handler
		next.ServeHTTP(w, r)
	})
}

func embedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the User-Agent header and convert it to lowercase for case-insensitive comparison
		userAgent := strings.ToLower(r.Header.Get("User-Agent"))

		// Check if the User-Agent contains "discordbot" (case-insensitive)
		if strings.Contains(userAgent, "discordbot") {
			// Set appropriate headers for HTML content and caching
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "max-age=3600") // Cache the response for 1 hour

			// Write the Open Graph meta tags for Discord embeds
			w.Write([]byte(`
                <!DOCTYPE html>
                <html lang="en">
                <head>
                    <meta charset="UTF-8">
                    <meta name="viewport" content="width=device-width, initial-scale=1.0">
                    <meta property="og:title" content="Blazium Engine">
                    <meta property="og:description" content="Blazium Engine forked from Godot.">
                    <meta property="og:image" content="https://blazium.app/static/assets/logo.png">
                    <meta property="og:url" content="https://blazium.app">
                    <meta property="og:type" content="website">
                    <meta name="twitter:card" content="summary_large_image">
                    <meta property="og:site_name" content="Blazium Engine">
                    <title>Blazium Engine</title>
                </head>
                <body>
                    <h1>Welcome to Blazium Engine</h1>
                </body>
                </html>
            `))
			return
		}

		// If the User-Agent is not from Discord, pass the request to the next handler
		next.ServeHTTP(w, r)
	})
}

// Legacy Discord handlers have been replaced by the new SlashCommandManager system
// The new system provides:
// - Proper slash command handling
// - Permission management
// - Server registration and management
// - Error handling and recovery
// - Comprehensive logging
