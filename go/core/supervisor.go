package core

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

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
	mu           sync.RWMutex
	wg           sync.WaitGroup
	taskStatus   TaskStatus
	poolManager  *PoolManager
	queueManager *QueueManager
	maxWorkers   int // Maximum number of workers allowed
}

// SupervisorConfig holds configuration for the supervisor
type SupervisorConfig struct {
	NumWorkers int
	MaxWorkers int // Maximum number of workers allowed
	APIKey     string
	WorkDir    string
}

// Update NewSupervisor function
func NewSupervisor(config SupervisorConfig) *Supervisor {
	if config.MaxWorkers == 0 {
		config.MaxWorkers = 15 // Default maximum workers
	}

	poolManager := NewPoolManager()
	queueManager := NewQueueManager(poolManager)

	supervisor := &Supervisor{
		workers:      make([]workerConnection, config.NumWorkers),
		workerProcs:  make([]*exec.Cmd, config.NumWorkers),
		tasks:        make(chan string, config.NumWorkers*2),
		results:      make(chan TaskResult, config.NumWorkers*2),
		apiKey:       config.APIKey,
		numWorkers:   config.NumWorkers,
		workDir:      config.WorkDir,
		poolManager:  poolManager,
		queueManager: queueManager,
		maxWorkers:   config.MaxWorkers,
	}

	// Set supervisor reference in queue manager
	queueManager.SetSupervisor(supervisor)

	return supervisor
}

func (s *Supervisor) startWorkerProcess(id int) error {
	workerBinary := filepath.Join(s.workDir, "worker")

	// Start the worker process
	cmd := exec.Command(workerBinary, "-id", fmt.Sprintf("%d", id))
	cmd.Env = append(os.Environ(), fmt.Sprintf("OPENAI_API_KEY=%s", s.apiKey))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = s.workDir // Set working directory

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start worker %d: %v", id, err)
	}

	s.workerProcs[id-1] = cmd
	log.Printf("[Supervisor] Started worker %d with PID %d", id, cmd.Process.Pid)
	return nil
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

// Start initializes workers and starts processing
func (s *Supervisor) Start(ctx context.Context) {
	log.Printf("[Supervisor] Starting supervisor with %d workers", s.numWorkers)

	// Check if worker binary already exists (e.g., in Docker)
	workerPath := filepath.Join(s.workDir, "worker")
	if _, err := os.Stat(workerPath); os.IsNotExist(err) {
		// Build worker binary only if it doesn't exist
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

	// Start worker processes
	for i := 1; i <= s.numWorkers; i++ {
		if err := s.startWorkerProcess(i); err != nil {
			log.Printf("[Supervisor] Error starting worker %d: %v", i, err)
			continue
		}
		// Register worker with pool manager
		s.poolManager.RegisterWorker(fmt.Sprintf("worker-%d", i), 1.0)
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
					if err := s.startWorkerProcess(i + 1); err != nil {
						log.Printf("[Supervisor] Failed to restart worker %d: %v", i+1, err)
						continue
					}

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
			if err := s.startWorkerProcess(idx + 1); err != nil {
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

				// Register and mark worker as available
				s.poolManager.RegisterWorker(fmt.Sprintf("worker-%d", idx+1), 1.0)
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
			MinimumAgreement: requirements.MinimumAgreement,
			TimeoutDuration:  time.Duration(requirements.TimeoutSeconds) * time.Second,
			VotingStrategy:   "majority",
			MatchStrategy:    requirements.MatchStrategy,
			NumericTolerance: requirements.NumericTolerance,
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
