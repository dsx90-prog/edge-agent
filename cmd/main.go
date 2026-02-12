package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"socket-proxy-service/internal/client"
	"socket-proxy-service/internal/config"
	"syscall"
	"time"
)

func main() {
	log.Println("Starting application...")

	// Parse flags FIRST before getting config
	flag.Parse()
	log.Println("Flags parsed")

	// Load configuration
	log.Println("Loading configuration...")
	cfg := config.GetConfig()
	log.Println("Configuration loaded")

	// Setup logging
	log.Println("Setting up logging...")
	setupLogging(cfg.Logging)
	log.Println("Logging setup complete")

	// Create socket client
	log.Println("Creating client...")
	client := client.NewClient(cfg)
	log.Println("Client created")

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start client
	log.Println("Starting client...")
	if err := client.Start(ctx); err != nil {
		log.Fatalf("Failed to start client: %v", err)
	}
	log.Println("Client started successfully")

	// Show initial status
	if cfg.WebSocket.Enabled {
		log.Printf("WebSocket client enabled - attempting to connect to %s", cfg.WebSocket.URL)
	} else {
		log.Println("WebSocket client disabled - running in standalone mode")
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("Socket proxy client is running. Press Ctrl+C to stop.")

	// Status ticker to show connection status
	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()

	for {
		select {
		case <-sigChan:
			log.Println("Shutdown signal received...")
			goto shutdown
		case <-statusTicker.C:
			stats := client.GetStats()
			log.Printf("stats: %s", stats)
			log.Printf("Status: Running=%v, WebSocket Connected=%v",
				stats["running"], stats["url"])
		}
	}

shutdown:

	// Graceful shutdown
	if err := client.Stop(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Println("Socket proxy client stopped")
}

func setupLogging(loggingConfig config.Logging) {
	// Set log flags
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// If log file is specified, create both console and file logging
	if loggingConfig.File != "" {
		// Try to open the file
		file, err := os.OpenFile(loggingConfig.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening log file %s: %v\n", loggingConfig.File, err)
			fmt.Fprintf(os.Stderr, "Continuing with console logging only...\n")
			return
		}

		// Test write to ensure file is writable
		if _, err := file.WriteString(""); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to log file %s: %v\n", loggingConfig.File, err)
			file.Close()
			fmt.Fprintf(os.Stderr, "Continuing with console logging only...\n")
			return
		}

		// Create a multi-writer that writes to both console and file
		multiWriter := io.MultiWriter(os.Stdout, file)
		log.SetOutput(multiWriter)
		fmt.Fprintf(os.Stderr, "Logging enabled: console + file (%s)\n", loggingConfig.File)
	}

	// Set log level if needed (simplified version)
	switch loggingConfig.Level {
	case "debug":
		log.Println("Debug logging enabled")
	case "info":
		log.Println("Info logging enabled")
	case "warn":
		log.Println("Warning logging enabled")
	case "error":
		log.Println("Error logging enabled")
	}
}
