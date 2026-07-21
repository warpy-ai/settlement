package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"settlement-core/consensus"
	pb "settlement-core/proto/gen/proto"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/connectivity"
)

// QueueManager handles the instruction queue and task distribution
type QueueManager struct {
	mu             sync.RWMutex
	instructions   []*Instruction
	poolManager    *PoolManager
	taskResults    map[string][]WorkerResult
	workerStatuses map[string]map[string]*WorkerStatusInfo // taskID -> workerID -> status info
	resultsChan    chan TaskResult
	consensusChan  chan *Instruction
	supervisor     *Supervisor
	retryHistory   map[string]map[string]bool // taskID -> workerID -> used
	maxRetries     int
}

// WorkerStatusInfo tracks detailed worker status during task processing
type WorkerStatusInfo struct {
	WorkerID  string
	Status    string  // "waiting", "processing", "completed", "failed"
	Progress  float64 // 0.0 to 1.0
	Reasoning string  // Worker's reasoning/thinking process
	Decision  string  // Worker's decision/answer
	Provider  string  // LLM provider (openai, anthropic, google, cohere, mistral)
	Model     string  // LLM model name
	UpdatedAt time.Time
}

// NewQueueManager creates a new queue manager instance
func NewQueueManager(poolManager *PoolManager) *QueueManager {
	return &QueueManager{
		instructions:   make([]*Instruction, 0),
		poolManager:    poolManager,
		taskResults:    make(map[string][]WorkerResult),
		workerStatuses: make(map[string]map[string]*WorkerStatusInfo),
		resultsChan:    make(chan TaskResult, 100),
		consensusChan:  make(chan *Instruction, 10),
		retryHistory:   make(map[string]map[string]bool),
		maxRetries:     3, // Maximum number of retry attempts
	}
}

// SetSupervisor sets the supervisor reference
func (qm *QueueManager) SetSupervisor(supervisor *Supervisor) {
	qm.supervisor = supervisor
}

// AddInstruction adds a new instruction to the queue
func (qm *QueueManager) AddInstruction(instruction *Instruction) error {
	if instruction.WorkerCount < 1 {
		return fmt.Errorf("worker count must be at least 1")
	}

	if instruction.WorkerCount%2 == 0 {
		return fmt.Errorf("worker count must be odd for consensus")
	}

	qm.mu.Lock()
	qm.instructions = append(qm.instructions, instruction)

	// Initialize worker statuses map early (before workers are assigned)
	// Create placeholder statuses so API can return worker count immediately
	if qm.workerStatuses[instruction.TaskID] == nil {
		qm.workerStatuses[instruction.TaskID] = make(map[string]*WorkerStatusInfo)
		// Create placeholder worker statuses based on expected worker count
		// These will be updated with actual worker IDs when workers are assigned
		for i := 0; i < instruction.WorkerCount; i++ {
			workerID := fmt.Sprintf("worker-%d", i+1)
			qm.workerStatuses[instruction.TaskID][workerID] = &WorkerStatusInfo{
				WorkerID:  workerID,
				Status:    "waiting",
				Progress:  0.0,
				UpdatedAt: time.Now(),
			}
		}
	}
	qm.mu.Unlock()

	// Start processing the instruction
	go qm.processInstruction(instruction)
	return nil
}

// processInstruction handles the execution of a single instruction
func (qm *QueueManager) processInstruction(instruction *Instruction) {
	// Always enable merged reasoning so downstream consumers get conversational output
	if !instruction.Consensus.ExtractMergedReasoning {
		log.Printf("[QueueManager] Enabling merged reasoning for task %s", instruction.TaskID)
		instruction.Consensus.ExtractMergedReasoning = true
	}

	retryCount := 0
	for retryCount <= qm.maxRetries {
		if retryCount > 0 {
			log.Printf("[QueueManager] Retry attempt %d for instruction %s", retryCount, instruction.TaskID)
		}

		// Initialize retry history for this task if not exists
		qm.mu.Lock()
		if _, exists := qm.retryHistory[instruction.TaskID]; !exists {
			qm.retryHistory[instruction.TaskID] = make(map[string]bool)
		}
		qm.mu.Unlock()

		// Calculate how many workers we need considering used workers
		qm.mu.RLock()
		usedWorkerCount := len(qm.retryHistory[instruction.TaskID])
		qm.mu.RUnlock()

		// Scale up workers if needed before trying to process
		currentWorkers := len(qm.supervisor.workers)
		requiredWorkers := instruction.WorkerCount + usedWorkerCount

		if currentWorkers < requiredWorkers && currentWorkers < qm.supervisor.maxWorkers {
			targetWorkers := min(requiredWorkers, qm.supervisor.maxWorkers)
			if err := qm.supervisor.scaleWorkers(context.Background(), targetWorkers); err != nil {
				log.Printf("[QueueManager] Failed to scale workers: %v", err)
			} else {
				log.Printf("[QueueManager] Scaled workers from %d to %d for retry (used workers: %d)",
					currentWorkers, targetWorkers, usedWorkerCount)
				time.Sleep(time.Second * 2) // Wait for workers to initialize
			}
		}

		success := qm.tryProcessInstruction(instruction, retryCount)
		if success {
			return
		}

		retryCount++
		if retryCount <= qm.maxRetries {
			time.Sleep(time.Second * 2) // Wait before retry
		}
	}

	log.Printf("[QueueManager] Failed to reach consensus for instruction %s after %d retries", instruction.TaskID, qm.maxRetries)
	// Fallback: synthesize a consensus using all available worker results via the AI merger
	qm.mu.RLock()
	availableResults := qm.taskResults[instruction.TaskID]
	qm.mu.RUnlock()

	if len(availableResults) > 0 {
		log.Printf("[QueueManager] Falling back to AI synthesis with %d worker results for task %s", len(availableResults), instruction.TaskID)
		instruction.Consensus.ExtractMergedReasoning = true
		if ok, result := qm.mergeConsensus(availableResults, instruction); ok {
			qm.resultsChan <- TaskResult{Result: result}
			return
		}
		log.Printf("[QueueManager] AI synthesis fallback failed for task %s", instruction.TaskID)
	}

	qm.resultsChan <- TaskResult{Error: fmt.Errorf("failed to reach consensus after %d retries", qm.maxRetries)}
}

// tryProcessInstruction attempts to process an instruction once
func (qm *QueueManager) tryProcessInstruction(instruction *Instruction, retryCount int) bool {
	log.Printf("[QueueManager] ===== Starting tryProcessInstruction for task %s (retry %d) =====", instruction.TaskID, retryCount)

	ctx, cancel := context.WithTimeout(context.Background(), instruction.Consensus.TimeoutDuration)
	defer cancel()

	// Get unused workers with exponential backoff
	var workers []*WorkerState
	maxAttempts := 5
	baseDelay := 500 * time.Millisecond

	qm.mu.RLock()
	usedWorkers := qm.retryHistory[instruction.TaskID]
	usedCount := len(usedWorkers)
	qm.mu.RUnlock()

	log.Printf("[QueueManager] Task %s: Need %d workers, %d already used in previous attempts", instruction.TaskID, instruction.WorkerCount, usedCount)

	log.Printf("[QueueManager] Attempting to get %d workers for task %s (retry %d)", instruction.WorkerCount, instruction.TaskID, retryCount)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Log worker pool status before getting workers
		total, available := qm.poolManager.GetWorkerCount()
		log.Printf("[QueueManager] Attempt %d: Pool has %d total workers, %d available (need %d)", attempt+1, total, available, instruction.WorkerCount)

		availableWorkers, getErr := qm.poolManager.GetAvailableWorkers(instruction.WorkerCount)
		if getErr != nil {
			log.Printf("[QueueManager] GetAvailableWorkers failed: %v", getErr)
			delay := time.Duration(1<<uint(attempt)) * baseDelay
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}

			// Check if we need to scale up
			total, available := qm.poolManager.GetWorkerCount()
			unusedCount := 0
			for _, w := range availableWorkers {
				if !usedWorkers[w.ID] {
					unusedCount++
				}
			}

			log.Printf("[QueueManager] Not enough unused workers (have %d, need %d), total=%d, available=%d",
				unusedCount, instruction.WorkerCount, total, available)

			// If we have less than needed workers and can scale up, do it immediately
			if unusedCount < instruction.WorkerCount {
				currentWorkers := len(qm.supervisor.workers)
				if currentWorkers < qm.supervisor.maxWorkers {
					targetWorkers := min(currentWorkers+instruction.WorkerCount, qm.supervisor.maxWorkers)
					if err := qm.supervisor.scaleWorkers(ctx, targetWorkers); err != nil {
						log.Printf("[QueueManager] Failed to scale workers: %v", err)
					} else {
						log.Printf("[QueueManager] Proactively scaled workers from %d to %d",
							currentWorkers, targetWorkers)
						time.Sleep(time.Second) // Brief wait for workers to initialize
					}
				}
			}

			// Force cleanup of stale workers before next attempt
			qm.poolManager.CleanupStaleWorkers()

			log.Printf("[QueueManager] Retrying worker allocation in %v...", delay)
			time.Sleep(delay)
			continue
		}

		// Filter out previously used workers
		unusedWorkers := make([]*WorkerState, 0)
		for _, w := range availableWorkers {
			if usedWorkers[w.ID] {
				log.Printf("[QueueManager] Skipping previously used worker %s", w.ID)
				continue
			}
			unusedWorkers = append(unusedWorkers, w)
		}

		// Group workers by provider for preference-based selection
		workersByProvider := make(map[string][]*WorkerState)
		for _, w := range unusedWorkers {
			provider := strings.ToLower(w.Provider)
			if provider == "" {
				provider = "unknown"
			}
			workersByProvider[provider] = append(workersByProvider[provider], w)
		}

		log.Printf("[QueueManager] Available workers by provider: %v", func() map[string]int {
			counts := make(map[string]int)
			for p, ws := range workersByProvider {
				counts[p] = len(ws)
			}
			return counts
		}())

		// Check if we have model preferences
		hasPreferences := instruction.ModelPreferences != nil && len(instruction.ModelPreferences) > 0
		if hasPreferences {
			log.Printf("[QueueManager] Model preferences specified: %v", instruction.ModelPreferences)
		}

		workers = make([]*WorkerState, instruction.WorkerCount)
		usedWorkerIDs := make(map[string]bool)
		providerCounts := make(map[string]int)

		// First pass: try to satisfy model preferences
		if hasPreferences {
			for position := 0; position < instruction.WorkerCount; position++ {
				preferredProvider, hasPreference := instruction.ModelPreferences[position]
				if !hasPreference || preferredProvider == "" || preferredProvider == "auto" {
					continue // Will be filled in second pass
				}

				preferredProvider = strings.ToLower(preferredProvider)
				providerWorkers := workersByProvider[preferredProvider]

				// Find an unused worker from the preferred provider
				for _, w := range providerWorkers {
					if usedWorkerIDs[w.ID] {
						continue
					}
					workers[position] = w
					usedWorkerIDs[w.ID] = true
					providerCounts[preferredProvider]++
					log.Printf("[QueueManager] Position %d: assigned preferred worker %s (provider=%s)", position, w.ID, preferredProvider)
					break
				}

				if workers[position] == nil {
					log.Printf("[QueueManager] Position %d: no available worker for preferred provider %s", position, preferredProvider)
				}
			}
		}

		// Second pass: fill remaining positions with available workers (applying diversity cap)
		// Shuffle remaining workers for randomness
		remainingWorkers := make([]*WorkerState, 0)
		for _, w := range unusedWorkers {
			if !usedWorkerIDs[w.ID] {
				remainingWorkers = append(remainingWorkers, w)
			}
		}
		rand.Shuffle(len(remainingWorkers), func(i, j int) {
			remainingWorkers[i], remainingWorkers[j] = remainingWorkers[j], remainingWorkers[i]
		})

		for position := 0; position < instruction.WorkerCount; position++ {
			if workers[position] != nil {
				continue // Already filled by preference
			}

			// Find an available worker respecting provider diversity (max 2 per provider)
			for _, w := range remainingWorkers {
				if usedWorkerIDs[w.ID] {
					continue
				}

				provider := strings.ToLower(w.Provider)
				if provider == "" {
					provider = "unknown"
				}

				if providerCounts[provider] >= 2 {
					continue // Skip due to provider cap
				}

				workers[position] = w
				usedWorkerIDs[w.ID] = true
				providerCounts[provider]++
				log.Printf("[QueueManager] Position %d: assigned worker %s (provider=%s, auto-selected)", position, w.ID, provider)
				break
			}

			// If still not filled, try again ignoring provider cap
			if workers[position] == nil {
				for _, w := range remainingWorkers {
					if usedWorkerIDs[w.ID] {
						continue
					}
					workers[position] = w
					usedWorkerIDs[w.ID] = true
					provider := strings.ToLower(w.Provider)
					if provider == "" {
						provider = "unknown"
					}
					providerCounts[provider]++
					log.Printf("[QueueManager] Position %d: assigned worker %s after relaxing provider cap", position, w.ID)
					break
				}
			}
		}

		// Convert to slice without nil entries and count filled positions
		filledWorkers := make([]*WorkerState, 0)
		for _, w := range workers {
			if w != nil {
				filledWorkers = append(filledWorkers, w)
			}
		}
		workers = filledWorkers

		// If we still don't have enough after both passes, retry the allocation loop.
		if len(workers) < instruction.WorkerCount {
			log.Printf("[QueueManager] Provider cap prevented filling all slots (got %d, need %d); retrying allocation", len(workers), instruction.WorkerCount)
			delay := time.Duration(1<<uint(attempt)) * baseDelay
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			time.Sleep(delay)
			continue
		}

		log.Printf("[QueueManager] Selected %d workers out of %d available (need %d)", len(workers), len(availableWorkers), instruction.WorkerCount)

		if len(workers) >= instruction.WorkerCount {
			log.Printf("[QueueManager] Successfully selected %d workers for task %s", len(workers), instruction.TaskID)
			break
		}

		delay := time.Duration(1<<uint(attempt)) * baseDelay
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}

		// If we don't have enough workers after scaling, wait briefly and retry
		time.Sleep(delay)
	}

	if len(workers) < instruction.WorkerCount {
		log.Printf("[QueueManager] Failed to get enough workers: have %d, need %d", len(workers), instruction.WorkerCount)
		return false
	}

	log.Printf("[QueueManager] Validating %d workers before assignment...", len(workers))

	// Verify workers are actually available and connected before assigning
	validWorkers := make([]*WorkerState, 0, len(workers))
	for _, w := range workers {
		// Check worker status
		status, err := qm.poolManager.GetWorkerStatus(w.ID)
		if err != nil {
			log.Printf("[QueueManager] Worker %s status check failed: %v, skipping", w.ID, err)
			continue
		}
		if status != "available" {
			log.Printf("[QueueManager] Worker %s is not available (status: %s), skipping", w.ID, status)
			continue
		}

		// Verify worker connection exists
		workerIDStr := w.ID[len("worker-"):]
		var workerIndex int
		if _, err := fmt.Sscanf(workerIDStr, "%d", &workerIndex); err != nil {
			log.Printf("[QueueManager] Invalid worker ID format: %s, skipping", w.ID)
			continue
		}
		workerIndex-- // Convert to 0-based

		qm.supervisor.mu.RLock()
		workersLen := len(qm.supervisor.workers)
		if workerIndex >= workersLen || workerIndex < 0 {
			qm.supervisor.mu.RUnlock()
			log.Printf("[QueueManager] Worker %s index out of range (%d not in [0, %d)), unregistering from pool", w.ID, workerIndex, workersLen)
			// Worker was scaled down but still in pool - unregister it
			qm.poolManager.UnregisterWorker(w.ID)
			continue
		}
		workerConn := qm.supervisor.workers[workerIndex]
		qm.supervisor.mu.RUnlock()

		if workerConn.client == nil {
			log.Printf("[QueueManager] Worker %s client is nil, skipping", w.ID)
			continue
		}

		validWorkers = append(validWorkers, w)
		log.Printf("[QueueManager] Worker %s validated successfully", w.ID)
	}

	// If we don't have enough valid workers, return false to retry
	if len(validWorkers) < instruction.WorkerCount {
		log.Printf("[QueueManager] Only %d valid workers out of %d needed (rejected %d), will retry",
			len(validWorkers), instruction.WorkerCount, len(workers)-len(validWorkers))
		return false
	}

	log.Printf("[QueueManager] All %d workers validated, proceeding with assignment", len(validWorkers))

	// Use only the valid workers (take first N if we have more)
	if len(validWorkers) > instruction.WorkerCount {
		validWorkers = validWorkers[:instruction.WorkerCount]
	}

	// Mark selected workers as used and assign task
	qm.mu.Lock()
	for _, w := range validWorkers {
		qm.retryHistory[instruction.TaskID][w.ID] = true
		if err := qm.poolManager.AssignTaskToWorker(w.ID, instruction.TaskID); err != nil {
			log.Printf("[QueueManager] Failed to assign task to worker %s: %v", w.ID, err)
		} else {
			log.Printf("[QueueManager] Successfully assigned task to worker %s", w.ID)
		}
	}
	qm.mu.Unlock()

	// Update workers list to use only valid workers
	workers = validWorkers

	// Create a WaitGroup for worker results
	var wg sync.WaitGroup
	wg.Add(len(workers))

	// Update worker statuses with actual assigned workers
	// (Placeholder statuses were already created in AddInstruction)
	qm.mu.Lock()
	if qm.workerStatuses[instruction.TaskID] == nil {
		qm.workerStatuses[instruction.TaskID] = make(map[string]*WorkerStatusInfo)
	}
	for _, worker := range workers {
		// Update existing placeholder or create new status
		if existing, exists := qm.workerStatuses[instruction.TaskID][worker.ID]; exists {
			// Update existing placeholder
			existing.Status = "waiting"
			existing.Progress = 0.0
			existing.UpdatedAt = time.Now()
			// Update provider/model if available
			if worker.Provider != "" {
				existing.Provider = worker.Provider
			}
			if worker.Model != "" {
				existing.Model = worker.Model
			}
		} else {
			// Create new status for this worker
			qm.workerStatuses[instruction.TaskID][worker.ID] = &WorkerStatusInfo{
				WorkerID:  worker.ID,
				Status:    "waiting",
				Progress:  0.0,
				Provider:  worker.Provider,
				Model:     worker.Model,
				UpdatedAt: time.Now(),
			}
		}
	}
	qm.mu.Unlock()

	// Process task with each worker
	for _, worker := range workers {
		go func(w *WorkerState) {
			defer wg.Done()
			defer func() {
				if err := qm.poolManager.ReleaseWorker(w.ID); err != nil {
					log.Printf("[QueueManager] Failed to release worker %s: %v", w.ID, err)
				}
			}()

			// Note: Worker is already validated and assigned before this goroutine starts
			// The worker status is "busy" at this point, which is expected
			// Update status to processing
			qm.updateWorkerStatus(instruction.TaskID, w.ID, "processing", 0.1, "", "")
			log.Printf("[QueueManager] Starting task processing for worker %s", w.ID)

			result, err := qm.processTaskWithWorker(ctx, instruction, w)
			if err != nil {
				log.Printf("[QueueManager] Worker %s failed to process task: %v", w.ID, err)
				qm.updateWorkerStatus(instruction.TaskID, w.ID, "failed", 0.0, "", err.Error())
				return
			}

			// Clean the response content (remove markdown code blocks if present)
			cleanedResult := cleanJSONResponse(result)

			var response WorkerResponse
			if err := json.Unmarshal([]byte(cleanedResult), &response); err != nil {
				log.Printf("[QueueManager] Worker %s failed to parse response: %v", w.ID, err)
				qm.updateWorkerStatus(instruction.TaskID, w.ID, "failed", 0.0, "", err.Error())
				return
			}

			// Update worker status with completed response
			qm.updateWorkerStatus(instruction.TaskID, w.ID, "completed", 1.0, response.Reasoning, response.Decision)

			qm.mu.Lock()
			qm.taskResults[instruction.TaskID] = append(qm.taskResults[instruction.TaskID], WorkerResult{
				WorkerID:    w.ID,
				Response:    &response,
				VotingPower: w.VotingPower,
				Timestamp:   time.Now(),
			})
			qm.mu.Unlock()
		}(worker)
	}

	// Wait for all workers to complete or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		log.Printf("[QueueManager] Instruction %s timed out on retry %d", instruction.TaskID, retryCount)
		// Mark any workers that didn't complete as failed
		qm.mu.RLock()
		for _, w := range workers {
			statusInfo, exists := qm.workerStatuses[instruction.TaskID][w.ID]
			if exists && statusInfo.Status != "completed" && statusInfo.Status != "failed" {
				log.Printf("[QueueManager] Marking worker %s as failed due to timeout", w.ID)
				qm.updateWorkerStatus(instruction.TaskID, w.ID, "failed", 0.0, "", "task timeout")
			}
		}
		qm.mu.RUnlock()
		// Force cleanup of any stuck workers
		qm.poolManager.CleanupStaleWorkers()
		return false
	case <-done:
		// Check if we have enough successful workers
		qm.mu.RLock()
		completedCount := 0
		failedCount := 0
		for _, w := range workers {
			if statusInfo, exists := qm.workerStatuses[instruction.TaskID][w.ID]; exists {
				if statusInfo.Status == "completed" {
					completedCount++
				} else if statusInfo.Status == "failed" {
					failedCount++
				}
			}
		}
		qm.mu.RUnlock()

		log.Printf("[QueueManager] Workers completed: %d, failed: %d, total: %d", completedCount, failedCount, len(workers))

		if completedCount < instruction.WorkerCount {
			log.Printf("[QueueManager] Not enough workers completed (%d/%d), cannot reach consensus", completedCount, instruction.WorkerCount)
			return false
		}

		consensus, result := qm.checkConsensus(instruction)
		if consensus {
			log.Printf("[QueueManager] Consensus reached for instruction %s on retry %d", instruction.TaskID, retryCount)
			qm.resultsChan <- TaskResult{Result: result}
			return true
		}
		log.Printf("[QueueManager] Failed to reach consensus for instruction %s on retry %d", instruction.TaskID, retryCount)
		return false
	}
}

// processTaskWithWorker executes a task on a specific worker using gRPC
func (qm *QueueManager) processTaskWithWorker(ctx context.Context, instruction *Instruction, worker *WorkerState) (string, error) {
	if qm.supervisor == nil {
		return "", fmt.Errorf("supervisor not set")
	}

	// Get worker index from ID (e.g., "worker-1" -> 0)
	workerIDStr := worker.ID[len("worker-"):]
	var workerIndex int
	_, err := fmt.Sscanf(workerIDStr, "%d", &workerIndex)
	if err != nil {
		return "", fmt.Errorf("invalid worker ID format: %s", worker.ID)
	}
	workerIndex-- // Convert to 0-based index

	// Get worker connection
	qm.supervisor.mu.RLock()
	if workerIndex >= len(qm.supervisor.workers) {
		qm.supervisor.mu.RUnlock()
		errMsg := fmt.Errorf("worker index out of range: %d (max: %d) for worker %s", workerIndex, len(qm.supervisor.workers), worker.ID)
		log.Printf("[QueueManager] %v", errMsg)
		return "", errMsg
	}
	workerConn := qm.supervisor.workers[workerIndex]
	qm.supervisor.mu.RUnlock()

	if workerConn.client == nil {
		errMsg := fmt.Errorf("worker %s (index %d) not connected - client is nil", worker.ID, workerIndex)
		log.Printf("[QueueManager] %v", errMsg)
		return "", errMsg
	}

	// Check connection state
	if workerConn.conn != nil {
		state := workerConn.conn.GetState()
		if state != connectivity.Ready && state != connectivity.Idle {
			errMsg := fmt.Errorf("worker %s connection not ready (state: %v)", worker.ID, state)
			log.Printf("[QueueManager] %v", errMsg)
			return "", errMsg
		}
	}

	// Get system prompt for this worker if specified
	var systemPrompt string
	if instruction.WorkerSystemPrompts != nil {
		systemPrompt = instruction.WorkerSystemPrompts[workerIndex]
	}

	// Create stream for task processing
	stream, err := workerConn.client.ProcessTask(ctx, &pb.TaskRequest{
		TaskId:       instruction.TaskID,
		Content:      instruction.Content,
		ApiKey:       qm.supervisor.apiKey,
		SystemPrompt: systemPrompt,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create stream: %v", err)
	}

	// Process responses
	for {
		resp, err := stream.Recv()
		if err != nil {
			return "", fmt.Errorf("stream error: %v", err)
		}

		switch resp.Status {
		case pb.WorkerStatus_COMPLETED:
			// Try to extract reasoning and decision from result
			var workerResp WorkerResponse
			if json.Unmarshal([]byte(cleanJSONResponse(resp.Result)), &workerResp) == nil {
				qm.updateWorkerStatus(instruction.TaskID, worker.ID, "completed", 1.0, workerResp.Reasoning, workerResp.Decision)
			}
			return resp.Result, nil
		case pb.WorkerStatus_FAILED:
			qm.updateWorkerStatus(instruction.TaskID, worker.ID, "failed", 0.0, "", resp.Error)
			return "", fmt.Errorf("%s", resp.Error)
		case pb.WorkerStatus_PROCESSING:
			// Update progress during processing
			qm.updateWorkerStatus(instruction.TaskID, worker.ID, "processing", 0.5, "", "")
			continue
		default:
			return "", fmt.Errorf("unknown status: %v", resp.Status)
		}
	}
}

// checkConsensus determines if workers have reached consensus on a task
func (qm *QueueManager) checkConsensus(instruction *Instruction) (bool, string) {
	qm.mu.RLock()
	results := qm.taskResults[instruction.TaskID]
	qm.mu.RUnlock()

	return consensus.Tally(context.Background(), instruction.Content, results, instruction.Consensus, qm.synthesizer())
}

// mergeConsensus synthesizes all worker responses into a single unified consensus answer
func (qm *QueueManager) mergeConsensus(results []WorkerResult, instruction *Instruction) (bool, string) {
	return consensus.Merge(context.Background(), instruction.Content, results, instruction.Consensus, qm.synthesizer())
}

// synthesizer adapts the supervisor's OpenAI helper to the consensus.Synthesizer seam.
// Returns nil (offline algorithmic fallbacks) when no API key is available.
func (qm *QueueManager) synthesizer() consensus.Synthesizer {
	if qm.supervisor == nil || qm.supervisor.apiKey == "" {
		return nil
	}
	return openAISynthesizer{apiKey: qm.supervisor.apiKey}
}

type openAISynthesizer struct{ apiKey string }

func (s openAISynthesizer) Complete(ctx context.Context, prompt string) (string, error) {
	return CallOpenAIFunction(ctx, prompt, s.apiKey)
}

// updateWorkerStatus updates the status of a worker for a specific task
func (qm *QueueManager) updateWorkerStatus(taskID, workerID, status string, progress float64, reasoning, decision string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if qm.workerStatuses[taskID] == nil {
		qm.workerStatuses[taskID] = make(map[string]*WorkerStatusInfo)
	}

	if qm.workerStatuses[taskID][workerID] == nil {
		// Try to get provider/model from pool manager
		provider := ""
		model := ""
		if qm.poolManager != nil {
			if workerState, err := qm.poolManager.GetWorkerByID(workerID); err == nil {
				provider = workerState.Provider
				model = workerState.Model
			}
		}

		qm.workerStatuses[taskID][workerID] = &WorkerStatusInfo{
			WorkerID: workerID,
			Provider: provider,
			Model:    model,
		}
	}

	info := qm.workerStatuses[taskID][workerID]
	info.Status = status
	info.Progress = progress
	info.UpdatedAt = time.Now()
	if reasoning != "" {
		info.Reasoning = reasoning
	}
	if decision != "" {
		info.Decision = decision
	}

	// Update provider/model if not set and we can get it from pool manager
	if (info.Provider == "" || info.Model == "") && qm.poolManager != nil {
		if workerState, err := qm.poolManager.GetWorkerByID(workerID); err == nil {
			if info.Provider == "" {
				info.Provider = workerState.Provider
			}
			if info.Model == "" {
				info.Model = workerState.Model
			}
		}
	}
}

// GetWorkerStatuses returns the current worker statuses for a task
func (qm *QueueManager) GetWorkerStatuses(taskID string) []WorkerStatusInfo {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	if qm.workerStatuses[taskID] == nil {
		return []WorkerStatusInfo{}
	}

	statuses := make([]WorkerStatusInfo, 0, len(qm.workerStatuses[taskID]))
	for _, info := range qm.workerStatuses[taskID] {
		statuses = append(statuses, *info)
	}

	// Sort by worker ID to ensure consistent ordering
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].WorkerID < statuses[j].WorkerID
	})

	return statuses
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// GetResults returns the channel for receiving task results
func (qm *QueueManager) GetResults() <-chan TaskResult {
	return qm.resultsChan
}

// GetPendingInstructions returns the current queue of pending instructions
func (qm *QueueManager) GetPendingInstructions() []*Instruction {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	pending := make([]*Instruction, len(qm.instructions))
	copy(pending, qm.instructions)
	return pending
}

// GetTaskResults returns the results for a specific task
func (qm *QueueManager) GetTaskResults(taskID string) []WorkerResult {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	results := make([]WorkerResult, len(qm.taskResults[taskID]))
	copy(results, qm.taskResults[taskID])
	return results
}
