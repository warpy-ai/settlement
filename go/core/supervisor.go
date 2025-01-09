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
type Supervisor struct {
	workers     []workerConnection
	workerProcs []*exec.Cmd
	tasks       chan string
	results     chan TaskResult
	apiKey      string
	numWorkers  int
	workDir     string
	mu          sync.RWMutex
	wg          sync.WaitGroup
	taskStatus  TaskStatus
}

// NewSupervisor creates a new supervisor instance
func NewSupervisor(numWorkers int, apiKey string) *Supervisor {
	workDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}

	return &Supervisor{
		workers:     make([]workerConnection, numWorkers),
		workerProcs: make([]*exec.Cmd, numWorkers),
		tasks:       make(chan string, numWorkers*2),
		results:     make(chan TaskResult, numWorkers*2),
		apiKey:      apiKey,
		numWorkers:  numWorkers,
		workDir:     workDir,
	}
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

	// Build worker binary
	buildCmd := exec.Command("go", "build", "-o", filepath.Join(s.workDir, "worker"), "cmd/worker/main.go")
	buildCmd.Dir = s.workDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	log.Printf("[Supervisor] Building worker binary in %s", s.workDir)
	if err := buildCmd.Run(); err != nil {
		log.Fatalf("[Supervisor] Failed to build worker binary: %v", err)
	}

	// Start worker processes
	for i := 1; i <= s.numWorkers; i++ {
		if err := s.startWorkerProcess(i); err != nil {
			log.Printf("[Supervisor] Error starting worker %d: %v", i, err)
			continue
		}
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
				return
			}
			log.Printf("[Supervisor] Failed to establish connection with worker %d after all retries", workerID+1)
		}(i)
	}

	// Wait for all connection attempts to complete
	connWg.Wait()

	// Start task processors
	for i := 0; i < s.numWorkers; i++ {
		s.wg.Add(1)
		go func(workerID int) {
			s.processWorkerTasks(ctx, workerID)
		}(i)
	}

	// Monitor worker processes
	go s.monitorWorkers(ctx)
}

func (s *Supervisor) monitorWorkers(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i, proc := range s.workerProcs {
				if proc == nil {
					continue
				}
				// Check if process is still running
				if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
					log.Printf("[Supervisor] Worker %d (PID %d) died, restarting...", i+1, proc.Process.Pid)
					if err := s.startWorkerProcess(i + 1); err != nil {
						log.Printf("[Supervisor] Failed to restart worker %d: %v", i+1, err)
					}
				}
			}
		}
	}
}

// Close initiates graceful shutdown
func (s *Supervisor) Close() {
	log.Printf("[Supervisor] Initiating supervisor shutdown (completed %d/%d tasks)",
		s.taskStatus.Completed, s.taskStatus.Total)
	close(s.tasks)

	// Wait for tasks to complete with timeout
	done := make(chan struct{})
	go func() {
		for {
			s.taskStatus.mu.Lock()
			if s.taskStatus.Completed >= s.taskStatus.Total {
				s.taskStatus.mu.Unlock()
				close(done)
				return
			}
			s.taskStatus.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		log.Printf("[Supervisor] All tasks completed successfully (%d/%d)",
			s.taskStatus.Completed, s.taskStatus.Total)
	case <-time.After(30 * time.Second):
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

	// Close results channel
	close(s.results)

	// Terminate worker processes
	for i, proc := range s.workerProcs {
		if proc != nil && proc.Process != nil {
			if err := proc.Process.Signal(syscall.SIGTERM); err != nil {
				log.Printf("[Supervisor] Failed to terminate worker %d: %v", i+1, err)
				proc.Process.Kill()
			}
		}
	}
}

// SubmitTask adds a task to the queue
func (s *Supervisor) SubmitTask(task string) {
	s.taskStatus.mu.Lock()
	s.taskStatus.Total++
	s.taskStatus.mu.Unlock()
	s.wg.Add(1)
	s.tasks <- task
}

// GetResults provides access to the results channel
func (s *Supervisor) GetResults() <-chan TaskResult {
	return s.results
}
