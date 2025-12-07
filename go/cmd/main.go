package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/rs/cors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"settlement-core/core"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	// Parse command line flags
	port := flag.Int("port", 8080, "API server port")
	flag.Parse()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Change to project root directory
	if err := os.Chdir(filepath.Join(filepath.Dir(filepath.Dir(os.Args[0])), "go")); err != nil {
		log.Printf("Warning: Failed to change to project directory: %v", err)
	}

	// Load .env file from the current directory
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable is required. Please set it in the .env file")
	}

	// Get LLM provider from environment (defaults to "random" for diversity)
	llmProvider := os.Getenv("LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "random"
	}

	// Get current working directory for WorkDir
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatal("Failed to get working directory:", err)
	}

	// Create a supervisor with configuration
	supervisor := core.NewSupervisor(core.SupervisorConfig{
		NumWorkers:  3,  // Start with minimum workers
		MaxWorkers:  15, // Allow scaling up to 15 workers
		APIKey:      apiKey,
		WorkDir:     workDir,
		LLMProvider: llmProvider,
	})

	// Create a context with timeout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the supervisor
	supervisor.Start(ctx)

	// Create and start the API server
	apiServer := core.NewAPIServer(supervisor)

	handler := cors.New(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3000", "http://localhost:5173"}, // Next.js and Vite
		AllowCredentials: true,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
	}).Handler(apiServer)

	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), handler); err != nil {
			log.Printf("API server error: %v", err)
			cancel()
		}
	}()

	log.Printf("API server started on port %d", *port)

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer shutdownCancel()

	// Cancel the main context to stop accepting new tasks
	cancel()

	// Wait for shutdown or timeout
	select {
	case <-shutdownCtx.Done():
		log.Println("Shutdown timeout reached")
	case <-time.After(time.Second):
		log.Println("Graceful shutdown completed")
	}
}
