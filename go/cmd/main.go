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

	// Get current working directory for WorkDir
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatal("Failed to get working directory:", err)
	}

	// Create a supervisor with configuration
	supervisor := core.NewSupervisor(core.SupervisorConfig{
		NumWorkers: 3,  // Start with minimum workers
		MaxWorkers: 15, // Allow scaling up to 15 workers
		APIKey:     apiKey,
		WorkDir:    workDir,
	})

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Start the supervisor
	supervisor.Start(ctx)

	// Example tasks with varying complexity and requirements
	tasks := []string{
		// Translation tasks (medium complexity, needs verification)
		"Translate this technical document to French: 'The quantum computer uses superposition and entanglement to perform parallel computations.'",
		"Translate this medical text to Spanish: 'The patient exhibits symptoms of acute respiratory distress syndrome.'",

		// Analysis tasks (high complexity, needs consensus)
		"Analyze the potential impact of artificial intelligence on employment in the next decade. Provide specific examples and statistics.",
		"Evaluate the environmental impact of electric vehicles compared to traditional combustion engines. Consider manufacturing, usage, and disposal.",

		// Creative tasks (high complexity, subjective)
		"Generate a creative story about a time traveler who discovers an ancient civilization on Mars.",

		// Mathematical tasks (low complexity, needs verification)
		"Calculate the compound interest on $10,000 invested for 5 years at 7% annual interest rate, compounded monthly.",

		// Research tasks (high complexity, needs verification)
		"Research and summarize the latest developments in fusion energy technology, focusing on breakthrough achievements in the past year.",
	}

	// Submit tasks with delay between submissions
	log.Printf("Submitting %d tasks for consensus processing", len(tasks))
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
	log.Println("All tasks submitted, waiting for consensus results")
	supervisor.Close()

	// Collect and print results with timeout
	successCount := 0
	errorCount := 0

	resultTimeout := time.After(4 * time.Minute) // Increased timeout for complex tasks
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
				log.Println("All consensus results received")
				goto SUMMARY
			}
			if result.Error != nil {
				log.Printf("Error in task: %v", result.Error)
				errorCount++
			} else {
				fmt.Printf("\nConsensus Result %d:\n%s\n", successCount+1, result.Result)
				successCount++
			}
		}
	}

SUMMARY:
	fmt.Printf("\nTask Summary:\n")
	fmt.Printf("Successful consensus: %d\n", successCount)
	fmt.Printf("Failed consensus: %d\n", errorCount)
	fmt.Printf("Total tasks: %d\n", successCount+errorCount)
}
