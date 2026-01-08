package core

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	llmclient "settlement-core/llm-client"
	pb "settlement-core/proto/gen/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

type workerConnection struct {
	client pb.WorkerServiceClient
	conn   *grpc.ClientConn
}

type TaskStatus struct {
	Completed int32
	Total     int32
	mu        sync.Mutex
}

// Supervisor manages worker agents
// Add these fields to your existing Supervisor struct
type Supervisor struct {
	workers      []workerConnection
	workerProcs  []*exec.Cmd
	tasks        chan string
	results      chan TaskResult
	apiKey       string
	numWorkers   int
	workDir      string
	llmProvider  string
	mu           sync.RWMutex
	wg           sync.WaitGroup
	taskStatus   TaskStatus
	poolManager  *PoolManager
	queueManager *QueueManager
	maxWorkers   int  // Maximum number of workers allowed
	lazyStart    bool // If true, workers are started on-demand only
	initialized  bool // Whether the supervisor has been initialized (worker binary built)
}

// SupervisorConfig holds configuration for the supervisor
type SupervisorConfig struct {
	NumWorkers  int
	MaxWorkers  int    // Maximum number of workers allowed
	APIKey      string
	WorkDir     string
	LLMProvider string // LLM provider: "openai", "anthropic", "google", "cohere", "mistral"
	LazyStart   bool   // If true, workers are not started at startup (on-demand only)
}

// NewSupervisor creates a new supervisor instance
func NewSupervisor(config SupervisorConfig) *Supervisor {
	if config.MaxWorkers == 0 {
		config.MaxWorkers = 15 // Default maximum workers
	}

	poolManager := NewPoolManager()
	queueManager := NewQueueManager(poolManager)

	// For lazy start, initialize with zero workers
	initialWorkers := config.NumWorkers
	if config.LazyStart {
		initialWorkers = 0
	}

	supervisor := &Supervisor{
		workers:      make([]workerConnection, initialWorkers),
		workerProcs:  make([]*exec.Cmd, initialWorkers),
		tasks:        make(chan string, config.NumWorkers*2),
		results:      make(chan TaskResult, config.NumWorkers*2),
		apiKey:       config.APIKey,
		numWorkers:   initialWorkers,
		workDir:      config.WorkDir,
		llmProvider:  config.LLMProvider,
		poolManager:  poolManager,
		queueManager: queueManager,
		maxWorkers:   config.MaxWorkers,
		lazyStart:    config.LazyStart,
		initialized:  false,
	}

	// Set supervisor reference in queue manager
	queueManager.SetSupervisor(supervisor)

	return supervisor
}

func (s *Supervisor) startWorkerProcess(id int) (llmclient.ModelConfig, error) {
	workerBinary := filepath.Join(s.workDir, "worker")

	// Pick provider/model for this worker
	cfg := s.pickLLMConfig()

	// Start the worker process
	cmd := exec.Command(workerBinary, "-id", fmt.Sprintf("%d", id))
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OPENAI_API_KEY=%s", os.Getenv("OPENAI_API_KEY")),
		fmt.Sprintf("ANTHROPIC_API_KEY=%s", os.Getenv("ANTHROPIC_API_KEY")),
		fmt.Sprintf("GOOGLE_API_KEY=%s", os.Getenv("GOOGLE_API_KEY")),
		fmt.Sprintf("COHERE_API_KEY=%s", os.Getenv("COHERE_API_KEY")),
		fmt.Sprintf("MISTRAL_API_KEY=%s", os.Getenv("MISTRAL_API_KEY")),
		fmt.Sprintf("LLM_PROVIDER=%s", cfg.Provider),
		fmt.Sprintf("LLM_MODEL=%s", cfg.Model),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = s.workDir // Set working directory

	if err := cmd.Start(); err != nil {
		return llmclient.ModelConfig{}, fmt.Errorf("failed to start worker %d: %v", id, err)
	}

	s.workerProcs[id-1] = cmd
	log.Printf("[Supervisor] Started worker %d with PID %d (provider=%s, model=%s)", id, cmd.Process.Pid, cfg.Provider, cfg.Model)
	return cfg, nil
}

// pickLLMConfig chooses a provider/model for a worker.
// Priority:
// 1) If llmProvider is empty or "random": pick random from reliable set (then full set fallback)
// 2) Otherwise: pick random model from the configured provider
func (s *Supervisor) pickLLMConfig() llmclient.ModelConfig {
	rand.Seed(time.Now().UnixNano())

	if s.llmProvider == "" || strings.ToLower(s.llmProvider) == "random" {
		reliable := llmclient.GetReliableModels()
		cfg := llmclient.GetRandomLLMConfigFromMap(reliable)
		if cfg.Provider == "" {
			cfg = llmclient.GetRandomLLMConfig()
		}
		return cfg
	}

	// Use configured provider, random model from that provider
	models := llmclient.GetModelsForProvider(llmclient.Provider(s.llmProvider))
	model := ""
	if len(models) > 0 {
		model = models[rand.Intn(len(models))]
	}
	return llmclient.ModelConfig{Provider: s.llmProvider, Model: model}
}

func (s *Supervisor) waitForWorkerHealth(ctx context.Context, port int, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			conn, err := grpc.Dial(fmt.Sprintf("localhost:%d", port),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithBlock(),
				grpc.WithTimeout(time.Second))
			if err != nil {
				lastErr = err
				time.Sleep(time.Second)
				continue
			}
			defer conn.Close()

			healthClient := grpc_health_v1.NewHealthClient(conn)
			resp, err := healthClient.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
			if err != nil {
				lastErr = err
				time.Sleep(time.Second)
				continue
			}

			if resp.Status == grpc_health_v1.HealthCheckResponse_SERVING {
				return nil
			}
		}
	}
	return fmt.Errorf("health check failed after %d retries: %v", maxRetries, lastErr)
}

func (s *Supervisor) connectToWorker(ctx context.Context, workerID int) error {
	port := 50051 + workerID
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx,
		fmt.Sprintf("localhost:%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)))
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}

	s.mu.Lock()
	s.workers[workerID] = workerConnection{
		client: pb.NewWorkerServiceClient(conn),
		conn:   conn,
	}
	s.mu.Unlock()

	return nil
}

func (s *Supervisor) ensureWorkerConnection(ctx context.Context, workerID int) error {
	s.mu.RLock()
	worker := s.workers[workerID]
	s.mu.RUnlock()

	if worker.conn == nil || worker.conn.GetState() == connectivity.Shutdown {
		return s.connectToWorker(ctx, workerID)
	}

	state := worker.conn.GetState()
	if state == connectivity.TransientFailure || state == connectivity.Idle {
		worker.conn.Connect()
	}

	return nil
}

func (s *Supervisor) processWorkerTasks(ctx context.Context, workerID int) {
	defer s.wg.Done()
	for task := range s.tasks {
		taskCtx, cancel := context.WithTimeout(ctx, 30*time.Second)

		// Ensure connection is valid
		if err := s.ensureWorkerConnection(taskCtx, workerID); err != nil {
			s.results <- TaskResult{
				Error: fmt.Errorf("worker %d connection error: %v", workerID+1, err),
			}
			s.taskStatus.markCompleted()
			cancel()
			continue
		}

		s.mu.RLock()
		worker := s.workers[workerID]
		s.mu.RUnlock()

		taskID := fmt.Sprintf("task-%d-%d", workerID+1, time.Now().UnixNano())
		stream, err := worker.client.ProcessTask(taskCtx, &pb.TaskRequest{
			TaskId:  taskID,
			Content: task,
			ApiKey:  s.apiKey,
		})

		if err != nil {
			s.results <- TaskResult{
				Error: fmt.Errorf("worker %d stream error: %v", workerID+1, err),
			}
			s.taskStatus.markCompleted()
			cancel()
			continue
		}

		for {
			resp, err := stream.Recv()
			if err != nil {
				cancel()
				break
			}

			switch resp.Status {
			case pb.WorkerStatus_COMPLETED:
				s.results <- TaskResult{Result: resp.Result}
				s.taskStatus.markCompleted()
				log.Printf("[Supervisor] Worker %d completed task: %s (%d/%d completed)",
					workerID+1, taskID, s.taskStatus.Completed, s.taskStatus.Total)
			case pb.WorkerStatus_FAILED:
				s.results <- TaskResult{Error: fmt.Errorf(resp.Error)}
				s.taskStatus.markCompleted()
				log.Printf("[Supervisor] Worker %d failed task: %s (%d/%d completed)",
					workerID+1, taskID, s.taskStatus.Completed, s.taskStatus.Total)
			}
		}
		cancel()
	}
}

func (ts *TaskStatus) markCompleted() {
	ts.mu.Lock()
	ts.Completed++
	ts.mu.Unlock()
}

// initializeWorkerBinary ensures the worker binary is built
func (s *Supervisor) initializeWorkerBinary() {
	if s.initialized {
		return
	}

	workerPath := filepath.Join(s.workDir, "worker")
	if _, err := os.Stat(workerPath); os.IsNotExist(err) {
		buildCmd := exec.Command("go", "build", "-o", workerPath, "cmd/worker/main.go")
		buildCmd.Dir = s.workDir
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr

		log.Printf("[Supervisor] Building worker binary in %s", s.workDir)
		if err := buildCmd.Run(); err != nil {
			log.Fatalf("[Supervisor] Failed to build worker binary: %v", err)
		}
	} else {
		log.Printf("[Supervisor] Using existing worker binary at %s", workerPath)
	}
	s.initialized = true
}

// startResultForwarding starts the goroutine that forwards results from queue manager
func (s *Supervisor) startResultForwarding(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Supervisor] Recovered from panic in result forwarding: %v", r)
			}
		}()

		queueResults := s.queueManager.GetResults()
		for {
			select {
			case <-ctx.Done():
				return
			case result, ok := <-queueResults:
				if !ok {
					return
				}
				select {
				case s.results <- result:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// Start initializes workers and starts processing
func (s *Supervisor) Start(ctx context.Context) {
	if s.lazyStart {
		log.Printf("[Supervisor] Starting in lazy mode - workers will be started on-demand")
		s.initializeWorkerBinary()
		s.startResultForwarding(ctx)
		return
	}

	log.Printf("[Supervisor] Starting supervisor with %d workers", s.numWorkers)
	s.initializeWorkerBinary()

	// Start worker processes
	for i := 1; i <= s.numWorkers; i++ {
		cfg, err := s.startWorkerProcess(i)
		if err != nil {
			log.Printf("[Supervisor] Error starting worker %d: %v", i, err)
			continue
		}
		// Register worker with pool manager (carry provider/model)
		s.poolManager.RegisterWorkerWithModel(fmt.Sprintf("worker-%d", i), 1.0, cfg.Provider, cfg.Model)
		// Give workers time to start
		time.Sleep(time.Second * 2)
	}

	// Wait for workers to be healthy and establish connections
	var connWg sync.WaitGroup
	connCtx, connCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connCancel()

	for i := 0; i < s.numWorkers; i++ {
		connWg.Add(1)
		go func(workerID int) {
			defer connWg.Done()

			// Wait for worker to be healthy with retries
			for retries := 0; retries < 10; retries++ {
				if err := s.waitForWorkerHealth(connCtx, 50051+workerID, 5); err != nil {
					log.Printf("[Supervisor] Worker %d health check attempt %d failed: %v", workerID+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				if err := s.connectToWorker(connCtx, workerID); err != nil {
					log.Printf("[Supervisor] Worker %d connection attempt %d failed: %v", workerID+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				log.Printf("[Supervisor] Successfully connected to worker %d", workerID+1)
				s.poolManager.UpdateWorkerStatus(fmt.Sprintf("worker-%d", workerID+1), "available")
				// Start heartbeat for each worker
				go s.workerHeartbeat(ctx, fmt.Sprintf("worker-%d", workerID+1))

				// Start task processing goroutine for this worker
				s.wg.Add(1)
				go s.processWorkerTasks(ctx, workerID)
				return
			}
			log.Printf("[Supervisor] Failed to establish connection with worker %d after all retries", workerID+1)
			s.poolManager.UpdateWorkerStatus(fmt.Sprintf("worker-%d", workerID+1), "offline")
		}(i)
	}

	// Wait for all connection attempts to complete
	connWg.Wait()

	// Start monitoring workers
	go s.monitorWorkers(ctx)

	// Forward results from queue manager to supervisor results channel
	s.startResultForwarding(ctx)
}

func (s *Supervisor) monitorWorkers(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			for i, proc := range s.workerProcs {
				if proc == nil {
					continue
				}
				// Check if process is still running
				if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
					log.Printf("[Supervisor] Worker %d (PID %d) died, restarting...", i+1, proc.Process.Pid)

					// Clean up old worker
					if s.workers[i].conn != nil {
						s.workers[i].conn.Close()
					}
					s.workers[i] = workerConnection{}
					s.workerProcs[i] = nil

					// Start new worker
					cfg, err := s.startWorkerProcess(i + 1)
					if err != nil {
						log.Printf("[Supervisor] Failed to restart worker %d: %v", i+1, err)
						continue
					}
					// Re-register with provider/model
					s.poolManager.RegisterWorkerWithModel(fmt.Sprintf("worker-%d", i+1), 1.0, cfg.Provider, cfg.Model)

					// Wait for worker to be ready
					go func(workerID int) {
						readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
						defer cancel()

						for retries := 0; retries < 5; retries++ {
							if err := s.waitForWorkerHealth(readyCtx, 50051+workerID, 5); err != nil {
								continue
							}
							if err := s.connectToWorker(readyCtx, workerID); err != nil {
								continue
							}
							s.poolManager.UpdateWorkerStatus(fmt.Sprintf("worker-%d", workerID+1), "available")
							return
						}
					}(i)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Close initiates graceful shutdown
func (s *Supervisor) Close() {
	log.Printf("[Supervisor] Initiating supervisor shutdown (completed %d/%d tasks)",
		s.taskStatus.Completed, s.taskStatus.Total)

	// Signal no more new tasks
	close(s.tasks)

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // Increased timeout
	defer cancel()

	// Wait for tasks to complete with timeout
	shutdownComplete := make(chan struct{})
	go func() {
		defer close(shutdownComplete)

		// Wait for all tasks to complete
		for {
			s.taskStatus.mu.Lock()
			completed := s.taskStatus.Completed
			total := s.taskStatus.Total
			s.taskStatus.mu.Unlock()

			if completed >= total {
				log.Printf("[Supervisor] All tasks completed successfully (%d/%d)",
					completed, total)
				return
			}

			// Check busy workers
			s.mu.RLock()
			busyWorkers := 0
			for i, worker := range s.workers {
				if worker.conn != nil {
					state, err := s.poolManager.GetWorkerStatus(fmt.Sprintf("worker-%d", i+1))
					if err == nil && state == "busy" {
						busyWorkers++
					}
				}
			}
			s.mu.RUnlock()

			log.Printf("[Supervisor] Waiting for tasks to complete (%d/%d), busy workers: %d",
				completed, total, busyWorkers)

			if busyWorkers == 0 && completed < total {
				// If no workers are busy but tasks are incomplete, something might be wrong
				log.Printf("[Supervisor] No busy workers but tasks incomplete (%d/%d), checking queue manager",
					completed, total)

				// Give queue manager a chance to retry
				time.Sleep(time.Second * 5)
				continue
			}

			time.Sleep(time.Second)
		}
	}()

	// Wait for shutdown or timeout
	select {
	case <-shutdownComplete:
		log.Printf("[Supervisor] Graceful shutdown completed")
	case <-shutdownCtx.Done():
		log.Printf("[Supervisor] Shutdown timeout reached, forcing shutdown (%d/%d completed)",
			s.taskStatus.Completed, s.taskStatus.Total)
	}

	// Close all connections
	s.mu.Lock()
	for i, worker := range s.workers {
		if worker.conn != nil {
			worker.conn.Close()
		}
		s.workers[i] = workerConnection{}
	}
	s.mu.Unlock()

	// Terminate worker processes gracefully
	for i, proc := range s.workerProcs {
		if proc != nil && proc.Process != nil {
			if err := proc.Process.Signal(syscall.SIGTERM); err != nil {
				log.Printf("[Supervisor] Failed to terminate worker %d gracefully: %v", i+1, err)
				proc.Process.Kill()
			}
			proc.Wait()
		}
	}

	// Close results channel after all cleanup
	close(s.results)
}

// scaleWorkers adjusts the number of workers to the specified target
func (s *Supervisor) scaleWorkers(ctx context.Context, targetWorkers int) error {
	s.mu.Lock()
	currentWorkers := len(s.workers)
	if targetWorkers == currentWorkers {
		s.mu.Unlock()
		return nil
	}

	log.Printf("[Supervisor] Scaling workers from %d to %d", currentWorkers, targetWorkers)

	// Scale down if needed
	if targetWorkers < currentWorkers {
		for i := currentWorkers - 1; i >= targetWorkers; i-- {
			workerID := fmt.Sprintf("worker-%d", i+1)
			// Unregister from pool manager first
			s.poolManager.UnregisterWorker(workerID)
			log.Printf("[Supervisor] Unregistered worker %s from pool", workerID)

			if s.workers[i].conn != nil {
				s.workers[i].conn.Close()
			}
			if s.workerProcs[i] != nil && s.workerProcs[i].Process != nil {
				s.workerProcs[i].Process.Signal(syscall.SIGTERM)
				s.workerProcs[i].Wait()
			}
		}
		s.workers = s.workers[:targetWorkers]
		s.workerProcs = s.workerProcs[:targetWorkers]
		s.mu.Unlock()
		return nil
	}

	// Prepare slices for new workers
	s.workers = append(s.workers, make([]workerConnection, targetWorkers-currentWorkers)...)
	s.workerProcs = append(s.workerProcs, make([]*exec.Cmd, targetWorkers-currentWorkers)...)
	s.mu.Unlock()

	// Start new workers
	var wg sync.WaitGroup
	errChan := make(chan error, targetWorkers-currentWorkers)

	for i := currentWorkers; i < targetWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Start new worker process
			cfg, err := s.startWorkerProcess(idx + 1)
			if err != nil {
				errChan <- fmt.Errorf("failed to start worker %d: %v", idx+1, err)
				return
			}

			// Give the worker process time to start
			time.Sleep(time.Second)

			// Wait for worker to be ready with timeout
			readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			var connected bool
			for retries := 0; retries < 10; retries++ {
				// Check health
				if err := s.waitForWorkerHealth(readyCtx, 50051+idx, 5); err != nil {
					log.Printf("[Supervisor] Worker %d health check attempt %d failed: %v", idx+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				// Try to connect
				if err := s.connectToWorker(readyCtx, idx); err != nil {
					log.Printf("[Supervisor] Worker %d connection attempt %d failed: %v", idx+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				// Register and mark worker as available (carry provider/model)
				s.poolManager.RegisterWorkerWithModel(fmt.Sprintf("worker-%d", idx+1), 1.0, cfg.Provider, cfg.Model)
				s.poolManager.UpdateWorkerStatus(fmt.Sprintf("worker-%d", idx+1), "available")
				log.Printf("[Supervisor] Successfully connected to worker %d", idx+1)

				// Start heartbeat goroutine for this worker
				go s.workerHeartbeat(ctx, fmt.Sprintf("worker-%d", idx+1))

				// Start task processing goroutine for this worker
				s.wg.Add(1)
				go s.processWorkerTasks(ctx, idx)

				connected = true
				break
			}

			if !connected {
				errChan <- fmt.Errorf("failed to establish connection with worker %d after all retries", idx+1)
			}
		}(i)
	}

	// Wait for all workers to be initialized
	wg.Wait()
	close(errChan)

	// Check for any errors
	var errors []string
	for err := range errChan {
		if err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to scale workers: %v", errors)
	}

	return nil
}

// workerHeartbeat maintains worker status as active
func (s *Supervisor) workerHeartbeat(ctx context.Context, workerID string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := s.poolManager.GetWorkerStatus(workerID)
			if err != nil || status == "offline" {
				return
			}
			s.poolManager.UpdateWorkerStatus(workerID, "available")
		}
	}
}

// SubmitTask adds a task to the queue with consensus requirements
func (s *Supervisor) SubmitTask(task string) {
	// Analyze task requirements
	ctx := context.Background()
	requirements, err := AnalyzeTask(ctx, task, s.apiKey)
	if err != nil {
		log.Printf("[Supervisor] Failed to analyze task: %v", err)
		s.results <- TaskResult{Error: fmt.Errorf("failed to analyze task: %v", err)}
		return
	}

	// Scale workers if needed
	if err := s.scaleWorkers(ctx, requirements.WorkerCount); err != nil {
		log.Printf("[Supervisor] Failed to scale workers: %v", err)
		s.results <- TaskResult{Error: fmt.Errorf("failed to scale workers: %v", err)}
		return
	}

	instruction := &Instruction{
		TaskID:      fmt.Sprintf("task-%d", time.Now().UnixNano()),
		Content:     task,
		WorkerCount: requirements.WorkerCount,
		Consensus: ConsensusConfig{
			MinimumAgreement:       requirements.MinimumAgreement,
			TimeoutDuration:        time.Duration(requirements.TimeoutSeconds) * time.Second,
			VotingStrategy:         "majority",
			MatchStrategy:          requirements.MatchStrategy,
			NumericTolerance:       requirements.NumericTolerance,
			ExtractMergedReasoning: true, // Always extract conversational response
		},
	}

	if err := s.queueManager.AddInstruction(instruction); err != nil {
		s.results <- TaskResult{Error: fmt.Errorf("failed to queue task: %v", err)}
		return
	}

	s.taskStatus.mu.Lock()
	s.taskStatus.Total++
	s.taskStatus.mu.Unlock()
}

// GetResults provides access to the results channel
func (s *Supervisor) GetResults() <-chan TaskResult {
	return s.results
}

// StartSessionWorkers starts workers on-demand for a session with specific configurations
func (s *Supervisor) StartSessionWorkers(ctx context.Context, session *SessionConfig) error {
	s.mu.Lock()
	currentWorkers := len(s.workers)
	targetWorkers := session.WorkerCount
	s.mu.Unlock()

	log.Printf("[Supervisor] Starting session workers: current=%d, target=%d", currentWorkers, targetWorkers)

	// Check if worker binary exists, build if needed
	workerPath := filepath.Join(s.workDir, "worker")
	if _, err := os.Stat(workerPath); os.IsNotExist(err) {
		buildCmd := exec.Command("go", "build", "-o", workerPath, "cmd/worker/main.go")
		buildCmd.Dir = s.workDir
		buildCmd.Stdout = os.Stdout
		buildCmd.Stderr = os.Stderr

		log.Printf("[Supervisor] Building worker binary in %s", s.workDir)
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("failed to build worker binary: %v", err)
		}
	}

	// Scale down existing workers if we have too many
	if currentWorkers > targetWorkers {
		s.mu.Lock()
		for i := currentWorkers - 1; i >= targetWorkers; i-- {
			workerID := fmt.Sprintf("worker-%d", i+1)
			s.poolManager.UnregisterWorker(workerID)
			if s.workers[i].conn != nil {
				s.workers[i].conn.Close()
			}
			if s.workerProcs[i] != nil && s.workerProcs[i].Process != nil {
				s.workerProcs[i].Process.Signal(syscall.SIGTERM)
				s.workerProcs[i].Wait()
			}
		}
		s.workers = s.workers[:targetWorkers]
		s.workerProcs = s.workerProcs[:targetWorkers]
		s.mu.Unlock()
	}

	// Prepare slices for new workers if needed
	if currentWorkers < targetWorkers {
		s.mu.Lock()
		s.workers = append(s.workers, make([]workerConnection, targetWorkers-currentWorkers)...)
		s.workerProcs = append(s.workerProcs, make([]*exec.Cmd, targetWorkers-currentWorkers)...)
		s.mu.Unlock()
	}

	// Start/reconfigure workers with session config
	var wg sync.WaitGroup
	errChan := make(chan error, targetWorkers)

	for i := 0; i < targetWorkers; i++ {
		workerConfig := session.GetWorkerConfig(i)

		// If worker already exists at this index, check if reconfiguration is needed
		s.mu.RLock()
		existingProc := s.workerProcs[i]
		s.mu.RUnlock()

		if existingProc != nil && existingProc.Process != nil {
			// Check if process is still running
			if err := existingProc.Process.Signal(syscall.Signal(0)); err == nil {
				// Worker is running, check if we need to reconfigure for different provider
				existingWorkerID := fmt.Sprintf("worker-%d", i+1)
				existingState, _ := s.poolManager.GetWorkerByID(existingWorkerID)

				// If the session requires a specific provider and it differs from current, restart worker
				if existingState != nil && !workerConfig.IsAutoProvider() {
					currentProvider := strings.ToLower(existingState.Provider)
					requestedProvider := strings.ToLower(workerConfig.Provider)
					if currentProvider != requestedProvider {
						log.Printf("[Supervisor] Worker %d needs provider change: %s -> %s, restarting...",
							i+1, currentProvider, requestedProvider)
						// Terminate existing worker
						s.poolManager.UnregisterWorker(existingWorkerID)
						if existingProc.Process != nil {
							existingProc.Process.Signal(syscall.SIGTERM)
							existingProc.Wait()
						}
						s.mu.Lock()
						s.workerProcs[i] = nil
						if s.workers[i].conn != nil {
							s.workers[i].conn.Close()
						}
						s.workers[i] = workerConnection{}
						s.mu.Unlock()
						// Fall through to start new worker with correct provider
					} else {
						// Provider matches, keep existing worker
						continue
					}
				} else {
					// No specific provider required or can't check, keep existing worker
					continue
				}
			}
		}

		wg.Add(1)
		go func(idx int, cfg *WorkerConfig) {
			defer wg.Done()

			// Start new worker process with session config
			modelCfg, err := s.startWorkerProcessWithConfig(idx+1, cfg)
			if err != nil {
				errChan <- fmt.Errorf("failed to start worker %d: %v", idx+1, err)
				return
			}

			// Give the worker process time to start
			time.Sleep(time.Second)

			// Wait for worker to be ready
			readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			var connected bool
			for retries := 0; retries < 10; retries++ {
				if err := s.waitForWorkerHealth(readyCtx, 50051+idx, 5); err != nil {
					log.Printf("[Supervisor] Worker %d health check attempt %d failed: %v", idx+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				if err := s.connectToWorker(readyCtx, idx); err != nil {
					log.Printf("[Supervisor] Worker %d connection attempt %d failed: %v", idx+1, retries+1, err)
					time.Sleep(time.Second)
					continue
				}

				// Register and mark worker as available
				s.poolManager.RegisterWorkerWithModel(fmt.Sprintf("worker-%d", idx+1), 1.0, modelCfg.Provider, modelCfg.Model)
				s.poolManager.UpdateWorkerStatus(fmt.Sprintf("worker-%d", idx+1), "available")
				log.Printf("[Supervisor] Successfully connected to worker %d", idx+1)

				// Start heartbeat goroutine
				go s.workerHeartbeat(ctx, fmt.Sprintf("worker-%d", idx+1))

				// Start task processing goroutine
				s.wg.Add(1)
				go s.processWorkerTasks(ctx, idx)

				connected = true
				break
			}

			if !connected {
				errChan <- fmt.Errorf("failed to establish connection with worker %d after all retries", idx+1)
			}
		}(i, workerConfig)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	var errors []string
	for err := range errChan {
		if err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to start session workers: %v", errors)
	}

	s.numWorkers = targetWorkers
	return nil
}

// startWorkerProcessWithConfig starts a worker process with specific configuration
func (s *Supervisor) startWorkerProcessWithConfig(id int, cfg *WorkerConfig) (llmclient.ModelConfig, error) {
	workerBinary := filepath.Join(s.workDir, "worker")

	// Determine provider and model
	var modelCfg llmclient.ModelConfig
	if cfg.IsAutoProvider() && cfg.IsAutoModel() {
		// Use random selection
		modelCfg = s.pickLLMConfig()
	} else {
		// Use specified config
		if !cfg.IsAutoProvider() {
			modelCfg.Provider = cfg.Provider
		} else {
			modelCfg.Provider = s.pickLLMConfig().Provider
		}
		if !cfg.IsAutoModel() {
			modelCfg.Model = cfg.Model
		} else {
			// Pick random model from the provider
			models := llmclient.GetModelsForProvider(llmclient.Provider(modelCfg.Provider))
			if len(models) > 0 {
				modelCfg.Model = models[rand.Intn(len(models))]
			}
		}
	}

	// Start the worker process
	cmd := exec.Command(workerBinary, "-id", fmt.Sprintf("%d", id))
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OPENAI_API_KEY=%s", os.Getenv("OPENAI_API_KEY")),
		fmt.Sprintf("ANTHROPIC_API_KEY=%s", os.Getenv("ANTHROPIC_API_KEY")),
		fmt.Sprintf("GOOGLE_API_KEY=%s", os.Getenv("GOOGLE_API_KEY")),
		fmt.Sprintf("COHERE_API_KEY=%s", os.Getenv("COHERE_API_KEY")),
		fmt.Sprintf("MISTRAL_API_KEY=%s", os.Getenv("MISTRAL_API_KEY")),
		fmt.Sprintf("LLM_PROVIDER=%s", modelCfg.Provider),
		fmt.Sprintf("LLM_MODEL=%s", modelCfg.Model),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = s.workDir

	if err := cmd.Start(); err != nil {
		return llmclient.ModelConfig{}, fmt.Errorf("failed to start worker %d: %v", id, err)
	}

	s.mu.Lock()
	s.workerProcs[id-1] = cmd
	s.mu.Unlock()

	log.Printf("[Supervisor] Started session worker %d with PID %d (provider=%s, model=%s)", id, cmd.Process.Pid, modelCfg.Provider, modelCfg.Model)
	return modelCfg, nil
}
