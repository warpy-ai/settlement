package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	pb "settlement-core/proto/gen/proto"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"google.golang.org/grpc/connectivity"
)

const mergedReasoningTimeout = 15 * time.Second

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

	// Create stream for task processing
	stream, err := workerConn.client.ProcessTask(ctx, &pb.TaskRequest{
		TaskId:  instruction.TaskID,
		Content: instruction.Content,
		ApiKey:  qm.supervisor.apiKey,
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
			return "", fmt.Errorf(resp.Error)
		case pb.WorkerStatus_PROCESSING:
			// Update progress during processing
			qm.updateWorkerStatus(instruction.TaskID, worker.ID, "processing", 0.5, "", "")
			continue
		default:
			return "", fmt.Errorf("unknown status: %v", resp.Status)
		}
	}
}

// normalizeText removes punctuation, extra spaces, and converts to lowercase
func normalizeText(text string) string {
	// Convert to lowercase
	text = strings.ToLower(text)

	// Remove punctuation and extra spaces
	text = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, text)

	// Split into words and remove common words that don't affect meaning
	words := strings.Fields(text)
	filtered := make([]string, 0, len(words))
	stopWords := map[string]bool{
		"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
		"be": true, "by": true, "for": true, "in": true, "is": true, "it": true,
		"of": true, "on": true, "or": true, "that": true, "the": true, "this": true,
		"to": true, "was": true, "were": true, "will": true, "with": true,
	}

	for _, word := range words {
		if !stopWords[word] {
			filtered = append(filtered, word)
		}
	}

	return strings.Join(filtered, " ")
}

// calculateSimilarity returns a similarity score between 0 and 1
func calculateSimilarity(text1, text2 string) float64 {
	words1 := strings.Fields(text1)
	words2 := strings.Fields(text2)

	// Create word frequency maps
	freq1 := make(map[string]int)
	freq2 := make(map[string]int)

	for _, word := range words1 {
		freq1[word]++
	}
	for _, word := range words2 {
		freq2[word]++
	}

	// Calculate intersection and union using word frequencies
	intersection := 0.0
	union := 0.0

	// Count intersection
	for word, count1 := range freq1 {
		if count2, exists := freq2[word]; exists {
			intersection += float64(min(count1, count2))
		}
		union += float64(count1)
	}

	// Add remaining words from freq2 to union
	for word, count2 := range freq2 {
		if _, exists := freq1[word]; !exists {
			union += float64(count2)
		}
	}

	if union == 0 {
		return 1.0
	}

	// Weight longer matches more heavily
	lengthFactor := float64(min(len(words1), len(words2))) / float64(max(len(words1), len(words2)))
	similarity := (intersection / union) * (0.7 + 0.3*lengthFactor)

	return similarity
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// mergeConsensus synthesizes all worker responses into a single unified consensus answer
func (qm *QueueManager) mergeConsensus(results []WorkerResult, instruction *Instruction) (bool, string) {
	if len(results) == 0 {
		return false, ""
	}

	// Collect all valid responses
	validResponses := make([]*WorkerResult, 0)
	totalVotingPower := 0.0
	totalConfidence := 0.0
	allAlternatives := make(map[string]bool) // Use map to deduplicate

	var responsesText strings.Builder
	responsesText.WriteString("The following are responses from multiple AI workers to the question: ")
	responsesText.WriteString(instruction.Content)
	responsesText.WriteString("\n\n")

	for i, result := range results {
		if result.Response == nil {
			continue
		}

		// Skip rate-limited responses
		if result.Response.Metadata != nil {
			if errorVal, ok := result.Response.Metadata["error"].(string); ok && strings.Contains(errorVal, "Rate limit reached") {
				continue
			}
		}

		validResponses = append(validResponses, &result)
		totalVotingPower += result.VotingPower
		totalConfidence += result.Response.Confidence * result.VotingPower

		// Build response text for synthesis
		responsesText.WriteString(fmt.Sprintf("Worker %d:\n", i+1))
		responsesText.WriteString(fmt.Sprintf("  Answer: %s\n", result.Response.Decision))
		responsesText.WriteString(fmt.Sprintf("  Reasoning: %s\n", result.Response.Reasoning))
		if len(result.Response.Alternatives) > 0 {
			responsesText.WriteString(fmt.Sprintf("  Alternatives: %s\n", strings.Join(result.Response.Alternatives, ", ")))
		}
		responsesText.WriteString("\n")

		// Collect alternatives
		for _, alt := range result.Response.Alternatives {
			allAlternatives[alt] = true
		}
	}

	if len(validResponses) == 0 {
		return false, ""
	}

	// Calculate average confidence
	avgConfidence := totalConfidence / totalVotingPower

	// First, try voting-based approach for deterministic consensus
	decisionVotes := make(map[string]float64)         // decision -> weighted votes
	decisionConfidences := make(map[string][]float64) // decision -> list of confidences

	for _, result := range validResponses {
		decision := result.Response.Decision
		weightedVote := result.VotingPower * result.Response.Confidence
		decisionVotes[decision] += weightedVote
		decisionConfidences[decision] = append(decisionConfidences[decision], result.Response.Confidence)
	}

	// Find the decision with the most votes
	bestDecision := ""
	maxVotes := 0.0
	totalVotes := 0.0
	for decision, votes := range decisionVotes {
		totalVotes += votes
		if votes > maxVotes {
			maxVotes = votes
			bestDecision = decision
		}
	}

	// If we have a clear winner (at least 40% of votes), use it directly
	useVoting := totalVotes > 0 && (maxVotes/totalVotes >= 0.4 || len(decisionVotes) == 1)

	if !useVoting {
		// Votes are split - use AI to synthesize a deterministic answer
		log.Printf("[QueueManager] Votes are split (%.1f%% for top answer), using AI synthesis", (maxVotes/totalVotes)*100)

		synthesisPrompt := fmt.Sprintf(`You are a consensus synthesizer. Multiple AI workers have provided different answers. You MUST pick ONE definitive answer.

CRITICAL RULES:
- Pick ONE answer definitively - do NOT say "it depends" or explain subjectivity
- Count which answer appears most frequently
- If similar answers exist, pick the most common one
- Be direct and concise (1-2 sentences maximum)
- Answer the question directly without hedging

Question: %s

Worker answers and their vote weights:
%s

Provide ONLY the definitive answer. No explanations about subjectivity. Just the answer.`, instruction.Content, func() string {
			var votesText strings.Builder
			for decision, votes := range decisionVotes {
				avgConf := 0.0
				if confs, ok := decisionConfidences[decision]; ok && len(confs) > 0 {
					for _, c := range confs {
						avgConf += c
					}
					avgConf /= float64(len(confs))
				}
				votesText.WriteString(fmt.Sprintf("- %.1f%% votes: %s (avg confidence: %.2f)\n", (votes/totalVotes)*100, decision, avgConf))
			}
			return votesText.String()
		}())

		// Get API key from supervisor
		if qm.supervisor == nil || qm.supervisor.apiKey == "" {
			log.Printf("[QueueManager] Cannot synthesize consensus: supervisor or API key not available")
			return qm.fallbackMergeConsensus(validResponses, allAlternatives, avgConfidence, instruction)
		}

		synthesizedDecision, err := CallOpenAIFunction(context.Background(), synthesisPrompt, qm.supervisor.apiKey)
		if err != nil {
			log.Printf("[QueueManager] Failed to synthesize consensus: %v", err)
			return qm.fallbackMergeConsensus(validResponses, allAlternatives, avgConfidence, instruction)
		}

		// Clean the synthesized response
		bestDecision = cleanJSONResponse(synthesizedDecision)

		// Validate synthesized answer
		if len(bestDecision) < 5 || len(bestDecision) > 500 {
			log.Printf("[QueueManager] Synthesized answer invalid, using fallback")
			return qm.fallbackMergeConsensus(validResponses, allAlternatives, avgConfidence, instruction)
		}
	} else {
		log.Printf("[QueueManager] Clear consensus winner found via voting: %.2f%% votes", (maxVotes/totalVotes)*100)
	}

	// Build merged reasoning that explains the consensus
	var mergedReasoning string
	if useVoting {
		mergedReasoning = fmt.Sprintf("Consensus reached from %d workers via voting (%.1f%% agreement). Average confidence: %.2f", len(validResponses), (maxVotes/totalVotes)*100, avgConfidence)
	} else {
		mergedReasoning = fmt.Sprintf("Consensus synthesized from %d workers. The unified answer incorporates common themes and agreements from all responses. Average confidence: %.2f", len(validResponses), avgConfidence)
	}

	// Determine category (use most common)
	categoryCount := make(map[string]int)
	for _, result := range validResponses {
		categoryCount[result.Response.Category]++
	}
	mostCommonCategory := "general"
	maxCount := 0
	for cat, count := range categoryCount {
		if count > maxCount {
			maxCount = count
			mostCommonCategory = cat
		}
	}

	// Convert alternatives map to slice
	alternatives := make([]string, 0, len(allAlternatives))
	for alt := range allAlternatives {
		alternatives = append(alternatives, alt)
	}

	synthesisType := "voting"
	if !useVoting {
		synthesisType = "ai_synthesized"
	}

	// Start parallel extraction of merged reasoning if enabled
	var mergedReasoningChan chan *MergedReasoning
	if instruction.Consensus.ExtractMergedReasoning && len(validResponses) > 1 {
		mergedReasoningChan = make(chan *MergedReasoning, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), mergedReasoningTimeout)
			defer cancel()
			mergedReasoningChan <- qm.extractMergedReasoning(
				ctx,
				validResponses,
				instruction.Content,
				bestDecision,
			)
		}()
	}

	consensusResponse := &WorkerResponse{
		Decision:   bestDecision,
		Confidence: avgConfidence,
		Category:   mostCommonCategory,
		Reasoning:  mergedReasoning,
		Metadata: map[string]interface{}{
			"consensus_strategy": "merge_match",
			"worker_count":       len(validResponses),
			"synthesis_type":     synthesisType,
			"vote_percentage":    (maxVotes / totalVotes) * 100,
		},
		Alternatives: alternatives,
	}

	// Wait for merged reasoning extraction with timeout
	if mergedReasoningChan != nil {
		select {
		case extracted := <-mergedReasoningChan:
			consensusResponse.MergedReasoning = extracted
		case <-time.After(mergedReasoningTimeout):
			log.Printf("[QueueManager] Merged reasoning extraction timed out in mergeConsensus for task %s, falling back to algorithmic", instruction.TaskID)
			if extracted, err := qm.extractMergedReasoningAlgorithmic(validResponses); err == nil {
				consensusResponse.MergedReasoning = extracted
			}
		}
	}

	resultBytes, err := json.Marshal(consensusResponse)
	if err != nil {
		log.Printf("[QueueManager] Failed to marshal merged consensus response: %v", err)
		return false, ""
	}

	log.Printf("[QueueManager] Synthesized consensus from %d workers into single unified answer", len(validResponses))
	return true, string(resultBytes)
}

// fallbackMergeConsensus provides a simple merge when AI synthesis fails
func (qm *QueueManager) fallbackMergeConsensus(validResponses []*WorkerResult, allAlternatives map[string]bool, avgConfidence float64, instruction *Instruction) (bool, string) {
	// Find most common decision using semantic similarity
	decisionCount := make(map[string]int)
	for _, result := range validResponses {
		normalized := normalizeText(result.Response.Decision)
		// Try to find similar existing decisions
		found := false
		for existing := range decisionCount {
			if calculateSimilarity(normalized, normalizeText(existing)) >= 0.7 {
				decisionCount[existing]++
				found = true
				break
			}
		}
		if !found {
			decisionCount[result.Response.Decision]++
		}
	}

	// Get the most common decision
	bestDecision := ""
	maxCount := 0
	for decision, count := range decisionCount {
		if count > maxCount {
			maxCount = count
			bestDecision = decision
		}
	}

	if bestDecision == "" && len(validResponses) > 0 {
		bestDecision = validResponses[0].Response.Decision
	}

	// Determine category
	categoryCount := make(map[string]int)
	for _, result := range validResponses {
		categoryCount[result.Response.Category]++
	}
	mostCommonCategory := "general"
	maxCatCount := 0
	for cat, count := range categoryCount {
		if count > maxCatCount {
			maxCatCount = count
			mostCommonCategory = cat
		}
	}

	alternatives := make([]string, 0, len(allAlternatives))
	for alt := range allAlternatives {
		alternatives = append(alternatives, alt)
	}

	consensusResponse := &WorkerResponse{
		Decision:   bestDecision,
		Confidence: avgConfidence,
		Category:   mostCommonCategory,
		Reasoning:  fmt.Sprintf("Consensus synthesized from %d workers (fallback merge)", len(validResponses)),
		Metadata: map[string]interface{}{
			"consensus_strategy": "merge_match",
			"worker_count":       len(validResponses),
			"synthesis_type":     "fallback",
		},
		Alternatives: alternatives,
	}

	// Extract merged reasoning using algorithmic method only (fast fallback)
	if instruction.Consensus.ExtractMergedReasoning && len(validResponses) > 1 {
		ctx, cancel := context.WithTimeout(context.Background(), mergedReasoningTimeout)
		defer cancel()
		if extracted := qm.extractMergedReasoning(ctx, validResponses, instruction.Content, bestDecision); extracted != nil {
			consensusResponse.MergedReasoning = extracted
		} else if alg, err := qm.extractMergedReasoningAlgorithmic(validResponses); err == nil {
			consensusResponse.MergedReasoning = alg
		}
	}

	resultBytes, err := json.Marshal(consensusResponse)
	if err != nil {
		return false, ""
	}

	return true, string(resultBytes)
}

// splitIntoSentences splits text into sentences for reasoning extraction
func splitIntoSentences(text string) []string {
	// Simple sentence splitting on common delimiters
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Replace common sentence-ending patterns with a delimiter
	delimiters := []string{". ", "! ", "? ", ".\n", "!\n", "?\n"}
	for _, d := range delimiters {
		text = strings.ReplaceAll(text, d, "|||")
	}

	parts := strings.Split(text, "|||")
	sentences := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) > 10 { // Skip very short fragments
			sentences = append(sentences, part)
		}
	}
	return sentences
}

// extractMergedReasoningAlgorithmic extracts unique reasoning contributions using text analysis
func (qm *QueueManager) extractMergedReasoningAlgorithmic(workers []*WorkerResult) (*MergedReasoning, error) {
	if len(workers) == 0 {
		return nil, fmt.Errorf("no workers provided")
	}

	contributions := make([]ReasoningContribution, 0)
	allSentences := make([]string, 0) // Track all sentences to check for uniqueness
	order := 1

	// Process each worker's reasoning
	for _, worker := range workers {
		if worker.Response == nil || worker.Response.Reasoning == "" {
			continue
		}

		sentences := splitIntoSentences(worker.Response.Reasoning)
		if len(sentences) == 0 {
			continue
		}

		// Find the most unique sentence from this worker
		var bestSentence string
		var bestUniqueness float64 = 0

		for _, sentence := range sentences {
			normalized := normalizeText(sentence)
			if normalized == "" {
				continue
			}

			// Calculate uniqueness as 1 - max similarity to existing sentences
			uniqueness := 1.0
			for _, existing := range allSentences {
				similarity := calculateSimilarity(normalized, existing)
				if 1-similarity < uniqueness {
					uniqueness = 1 - similarity
				}
			}

			// Prefer longer, more unique sentences
			lengthBonus := float64(len(sentence)) / 200.0 // Bonus for length up to 200 chars
			if lengthBonus > 0.3 {
				lengthBonus = 0.3
			}
			score := uniqueness + lengthBonus

			if score > bestUniqueness {
				bestUniqueness = score
				bestSentence = sentence
			}
		}

		// Add the best unique sentence if it's sufficiently unique (> 0.4 uniqueness)
		if bestSentence != "" && bestUniqueness > 0.4 {
			contributions = append(contributions, ReasoningContribution{
				WorkerID:   worker.WorkerID,
				Text:       bestSentence,
				Confidence: worker.Response.Confidence,
				Order:      order,
			})
			allSentences = append(allSentences, normalizeText(bestSentence))
			order++
		}
	}

	// Build summary from contributions
	var summaryBuilder strings.Builder
	for i, contrib := range contributions {
		if i > 0 {
			summaryBuilder.WriteString(" ")
		}
		summaryBuilder.WriteString(contrib.Text)
		if !strings.HasSuffix(contrib.Text, ".") && !strings.HasSuffix(contrib.Text, "!") && !strings.HasSuffix(contrib.Text, "?") {
			summaryBuilder.WriteString(".")
		}
		if i >= 2 { // Limit summary to first 3 contributions
			break
		}
	}

	return &MergedReasoning{
		Summary:       summaryBuilder.String(),
		Contributions: contributions,
		WorkerCount:   len(workers),
		SynthesisType: "algorithmic",
	}, nil
}

// extractMergedReasoningAI uses LLM to synthesize a conversational response from worker reasoning
func (qm *QueueManager) extractMergedReasoningAI(ctx context.Context, workers []*WorkerResult, question, decision string) (*MergedReasoning, error) {
	if qm.supervisor == nil || qm.supervisor.apiKey == "" {
		return nil, fmt.Errorf("no API key available for AI extraction")
	}

	// Build prompt with worker reasonings for conversational synthesis
	var promptBuilder strings.Builder
	promptBuilder.WriteString(`You are "The Council" - a wise gathering of AI advisors helping users with their questions.
Multiple council members have deliberated and reached consensus. Your task is to synthesize their reasoning into a single, natural conversational response.

`)
	promptBuilder.WriteString(fmt.Sprintf("User's Question: %s\n\n", question))
	promptBuilder.WriteString(fmt.Sprintf("Council's Decision: %s\n\n", decision))
	promptBuilder.WriteString("Council Members' Reasoning:\n")

	for i, worker := range workers {
		if worker.Response == nil || worker.Response.Reasoning == "" {
			continue
		}
		promptBuilder.WriteString(fmt.Sprintf("Councillor %d (confidence: %.0f%%):\n%s\n\n",
			i+1, worker.Response.Confidence*100, worker.Response.Reasoning))
	}

	promptBuilder.WriteString(`Create a unified response that:
1. Sounds like a single wise advisor speaking naturally
2. Incorporates the best insights from each council member
3. Is conversational and helpful (like ChatGPT)
4. Addresses the user directly
5. Is concise but thorough

Return ONLY valid JSON:
{
  "conversational_response": "Your natural, conversational response to the user that synthesizes all council reasoning into one cohesive answer",
  "summary": "Brief 1-2 sentence summary of the key points",
  "contributions": [
    {"worker_id": "councillor-1", "text": "key insight from this member", "order": 1}
  ]
}`)

	// Call OpenAI for synthesis
	response, err := CallOpenAIFunction(ctx, promptBuilder.String(), qm.supervisor.apiKey)
	if err != nil {
		return nil, fmt.Errorf("AI extraction failed: %w", err)
	}

	// Parse the JSON response
	response = cleanJSONResponse(response)

	var aiResult struct {
		ConversationalResponse string `json:"conversational_response"`
		Summary                string `json:"summary"`
		Contributions          []struct {
			WorkerID string `json:"worker_id"`
			Text     string `json:"text"`
			Order    int    `json:"order"`
		} `json:"contributions"`
	}

	if err := json.Unmarshal([]byte(response), &aiResult); err != nil {
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	// Build worker confidence map for lookup
	confidenceMap := make(map[string]float64)
	workerIDMap := make(map[int]string) // Map councillor number to actual worker ID
	for i, worker := range workers {
		if worker.Response != nil {
			confidenceMap[worker.WorkerID] = worker.Response.Confidence
			workerIDMap[i+1] = worker.WorkerID
		}
	}

	// Convert to MergedReasoning with proper worker IDs
	contributions := make([]ReasoningContribution, 0, len(aiResult.Contributions))
	for _, c := range aiResult.Contributions {
		// Try to extract councillor number from worker_id like "councillor-1"
		actualWorkerID := c.WorkerID
		if strings.HasPrefix(c.WorkerID, "councillor-") {
			numStr := strings.TrimPrefix(c.WorkerID, "councillor-")
			if num, err := strconv.Atoi(numStr); err == nil {
				if realID, ok := workerIDMap[num]; ok {
					actualWorkerID = realID
				}
			}
		}

		contributions = append(contributions, ReasoningContribution{
			WorkerID:   actualWorkerID,
			Text:       c.Text,
			Confidence: confidenceMap[actualWorkerID],
			Order:      c.Order,
		})
	}

	return &MergedReasoning{
		Summary:                aiResult.Summary,
		Contributions:          contributions,
		WorkerCount:            len(workers),
		SynthesisType:          "ai",
		ConversationalResponse: aiResult.ConversationalResponse,
	}, nil
}

// isLowQualityMergedReasoning checks if the algorithmic result needs AI fallback
func isLowQualityMergedReasoning(result *MergedReasoning) bool {
	if result == nil {
		return true
	}
	// Low quality if less than 2 contributions or short summary
	if len(result.Contributions) < 2 {
		return true
	}
	if len(result.Summary) < 50 {
		return true
	}
	// Check for empty contributions
	for _, c := range result.Contributions {
		if c.Text == "" {
			return true
		}
	}
	return false
}

// extractMergedReasoning extracts merged reasoning using LLM-first approach for conversational responses
func (qm *QueueManager) extractMergedReasoning(ctx context.Context, workers []*WorkerResult, question, decision string) *MergedReasoning {
	// Always try AI extraction first for conversational responses
	log.Printf("[QueueManager] Using LLM for conversational reasoning synthesis")
	aiResult, err := qm.extractMergedReasoningAI(ctx, workers, question, decision)
	if err != nil {
		log.Printf("[QueueManager] AI reasoning extraction failed: %v, falling back to algorithmic", err)
		// Fall back to algorithmic extraction if AI fails
		result, algErr := qm.extractMergedReasoningAlgorithmic(workers)
		if algErr != nil {
			log.Printf("[QueueManager] Algorithmic extraction also failed: %v", algErr)
			return nil
		}
		return result
	}

	return aiResult
}

// checkConsensus determines if workers have reached consensus on a task
func (qm *QueueManager) checkConsensus(instruction *Instruction) (bool, string) {
	qm.mu.RLock()
	results := qm.taskResults[instruction.TaskID]
	qm.mu.RUnlock()

	if len(results) == 0 {
		return false, ""
	}

	// Handle merge_match strategy: merge all responses for subjective questions
	if instruction.Consensus.MatchStrategy == MergeMatch {
		return qm.mergeConsensus(results, instruction)
	}

	// Group responses by decision and calculate weighted votes
	type consensusGroup struct {
		totalVotes  float64
		responses   []*WorkerResult
		confidence  float64
		rateLimited bool
	}

	groups := make(map[string]*consensusGroup)
	totalVotingPower := 0.0
	rateLimitedCount := 0

	// First pass: Create initial groups
	for _, result := range results {
		if result.Response == nil {
			continue
		}

		// Check for rate limit errors
		if result.Response.Metadata != nil {
			if errorVal, ok := result.Response.Metadata["error"].(string); ok && strings.Contains(errorVal, "Rate limit reached") {
				rateLimitedCount++
				continue
			}
		}

		key := result.Response.Decision
		switch instruction.Consensus.MatchStrategy {
		case NumericMatch:
			// For numeric results, group within tolerance
			value, err := parseNumericValue(result.Response.Decision)
			if err != nil {
				log.Printf("[QueueManager] Failed to parse numeric value: %v", err)
				continue
			}
			found := false
			for existingKey := range groups {
				existingValue, _ := parseNumericValue(existingKey)
				if math.Abs(value-existingValue) <= instruction.Consensus.NumericTolerance {
					key = existingKey
					found = true
					break
				}
			}
			if !found {
				key = result.Response.Decision
			}

		case SemanticMatch:
			// For semantic match, normalize and find similar groups
			normalizedKey := normalizeText(key)
			bestMatch := key
			highestSimilarity := 0.0

			for existingKey := range groups {
				similarity := calculateSimilarity(normalizedKey, normalizeText(existingKey))
				if similarity >= 0.6 && similarity > highestSimilarity { // Lowered threshold
					bestMatch = existingKey
					highestSimilarity = similarity
				}
			}
			key = bestMatch
		}

		weightedVote := result.VotingPower * result.Response.Confidence
		if group, exists := groups[key]; exists {
			group.totalVotes += weightedVote
			group.responses = append(group.responses, &result)
			group.confidence = (group.confidence*float64(len(group.responses)-1) + result.Response.Confidence) / float64(len(group.responses))
		} else {
			groups[key] = &consensusGroup{
				totalVotes: weightedVote,
				responses:  []*WorkerResult{&result},
				confidence: result.Response.Confidence,
			}
		}
		totalVotingPower += result.VotingPower
	}

	// Second pass: Merge very similar groups
	if instruction.Consensus.MatchStrategy == SemanticMatch {
		merged := true
		for merged {
			merged = false
			for key1, group1 := range groups {
				for key2, group2 := range groups {
					if key1 == key2 {
						continue
					}
					if similarity := calculateSimilarity(normalizeText(key1), normalizeText(key2)); similarity >= 0.8 {
						// Merge group2 into group1
						group1.totalVotes += group2.totalVotes
						group1.responses = append(group1.responses, group2.responses...)
						group1.confidence = (group1.confidence*float64(len(group1.responses)-len(group2.responses)) +
							group2.confidence*float64(len(group2.responses))) / float64(len(group1.responses))
						delete(groups, key2)
						merged = true
						break
					}
				}
				if merged {
					break
				}
			}
		}
	}

	// If too many rate limits, return false to retry
	if float64(rateLimitedCount)/float64(len(results)) > 0.5 {
		log.Printf("[QueueManager] Too many rate limited responses (%d/%d), will retry",
			rateLimitedCount, len(results))
		return false, ""
	}

	// Find the group with the highest weighted votes
	var bestResult string
	var highestVotes float64
	var bestGroup *consensusGroup
	var secondHighestVotes float64

	for decision, group := range groups {
		weightedVotes := group.totalVotes / totalVotingPower
		log.Printf("[QueueManager] Group '%s' has %.2f%% agreement (confidence: %.2f)",
			decision, weightedVotes*100, group.confidence)

		if weightedVotes > highestVotes {
			secondHighestVotes = highestVotes
			highestVotes = weightedVotes
			bestResult = decision
			bestGroup = group
		} else if weightedVotes > secondHighestVotes {
			secondHighestVotes = weightedVotes
		}
	}

	// Check if the highest vote percentage is significantly higher than the second highest
	// This ensures we have a clear winner
	voteDifference := highestVotes - secondHighestVotes
	hasSignificantLead := voteDifference >= 0.1 // At least 10% higher than the next best

	// Adjust minimum agreement based on number of groups
	adjustedMinAgreement := instruction.Consensus.MinimumAgreement
	if len(groups) > 2 {
		// Lower the threshold when there are many similar valid answers
		adjustedMinAgreement *= 0.6
	}

	// Accept the result if it meets the minimum agreement OR has a significant lead
	if highestVotes >= adjustedMinAgreement || hasSignificantLead {
		// Start parallel extraction of merged reasoning if enabled
		var mergedReasoningChan chan *MergedReasoning
		if instruction.Consensus.ExtractMergedReasoning && len(bestGroup.responses) > 1 {
			mergedReasoningChan = make(chan *MergedReasoning, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), mergedReasoningTimeout)
				defer cancel()
				mergedReasoningChan <- qm.extractMergedReasoning(
					ctx,
					bestGroup.responses,
					instruction.Content,
					bestResult,
				)
			}()
		}

		consensusResponse := &WorkerResponse{
			Decision:   bestResult,
			Confidence: bestGroup.confidence,
			Category:   bestGroup.responses[0].Response.Category,
			Reasoning: fmt.Sprintf("Consensus reached with %.2f%% agreement among %d workers (lead: %.2f%%)",
				highestVotes*100, len(bestGroup.responses), voteDifference*100),
			Metadata: map[string]interface{}{
				"consensus_strategy":  string(instruction.Consensus.MatchStrategy),
				"worker_count":        len(results),
				"agreeing_workers":    len(bestGroup.responses),
				"agreement_threshold": adjustedMinAgreement,
				"actual_agreement":    highestVotes,
				"vote_difference":     voteDifference,
				"total_groups":        len(groups),
			},
		}

		// Collect alternative answers from other groups
		for decision, group := range groups {
			if decision != bestResult {
				consensusResponse.Alternatives = append(
					consensusResponse.Alternatives,
					fmt.Sprintf("%s (%.2f%% agreement, confidence: %.2f)",
						decision, (group.totalVotes/totalVotingPower)*100, group.confidence),
				)
			}
		}

		// Wait for merged reasoning extraction with timeout
		if mergedReasoningChan != nil {
			select {
			case merged := <-mergedReasoningChan:
				consensusResponse.MergedReasoning = merged
			case <-time.After(mergedReasoningTimeout):
				log.Printf("[QueueManager] Merged reasoning extraction timed out for task %s, falling back to algorithmic", instruction.TaskID)
				if extracted, err := qm.extractMergedReasoningAlgorithmic(bestGroup.responses); err == nil {
					consensusResponse.MergedReasoning = extracted
				}
			}
		}

		resultBytes, err := json.Marshal(consensusResponse)
		if err != nil {
			log.Printf("[QueueManager] Failed to marshal consensus response: %v", err)
			return false, ""
		}

		return true, string(resultBytes)
	}

	return false, ""
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

// parseNumericValue attempts to extract a numeric value from a string
func parseNumericValue(s string) (float64, error) {
	// Remove any currency symbols, commas, and other non-numeric characters
	s = strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) || r == '.' || r == '-' {
			return r
		}
		return -1
	}, s)

	return strconv.ParseFloat(s, 64)
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
