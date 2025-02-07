package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"settlement-core/core"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

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

	// Create a supervisor with 3 workers
	supervisor := core.NewSupervisor(7, apiKey)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Start the supervisor
	supervisor.Start(ctx)

	// Example tasks
	tasks := []string{
		"Translate the following text to French: 'Hello, world!'",
		"Summarize this text: OpenAI has released new API features that enhance function calling capabilities.",
		"Generate a list of 5 popular programming languages in 2024.",
		"What is the capital of France?",
		"Calculate 15% of 85.",
		"What is the capital of Germany?",
		"Calculate 10% of 85.",
	}

	// Submit tasks with delay between submissions
	log.Printf("Submitting %d tasks", len(tasks))
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled while submitting tasks: %v", ctx.Err())
			return
		case <-sigChan:
			log.Println("Received shutdown signal while submitting tasks")
			return
		default:
			supervisor.SubmitTask(task)
			time.Sleep(100 * time.Millisecond) // Add small delay between task submissions
		}
	}

	// Close tasks channel after submitting all tasks
	log.Println("All tasks submitted, waiting for completion")
	supervisor.Close()

	// Collect and print results with timeout
	successCount := 0
	errorCount := 0

	resultTimeout := time.After(2 * time.Minute)
	resultsChan := supervisor.GetResults()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled while collecting results: %v", ctx.Err())
			goto SUMMARY
		case <-resultTimeout:
			log.Println("Timeout while waiting for results")
			goto SUMMARY
		case <-sigChan:
			log.Println("Received shutdown signal while collecting results")
			goto SUMMARY
		case result, ok := <-resultsChan:
			if !ok {
				// Channel closed, all results received
				log.Println("All results received")
				goto SUMMARY
			}
			if result.Error != nil {
				log.Printf("Error in task: %v", result.Error)
				errorCount++
			} else {
				fmt.Printf("Result %d: %s\n", successCount+1, result.Result)
				successCount++
			}
		}
	}

SUMMARY:
	fmt.Printf("\nTask Summary:\n")
	fmt.Printf("Successful: %d\n", successCount)
	fmt.Printf("Failed: %d\n", errorCount)
	fmt.Printf("Total: %d\n", successCount+errorCount)
}
