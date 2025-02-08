package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	pb "settlement-core/proto/gen/proto"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// QueueManager handles the instruction queue and task distribution
type QueueManager struct {
	mu            sync.RWMutex
	instructions  []*Instruction
	poolManager   *PoolManager
	taskResults   map[string][]WorkerResult
	resultsChan   chan TaskResult
	consensusChan chan *Instruction
	supervisor    *Supervisor
	retryHistory  map[string]map[string]bool // taskID -> workerID -> used
	maxRetries    int
}

// NewQueueManager creates a new queue manager instance
func NewQueueManager(poolManager *PoolManager) *QueueManager {
	return &QueueManager{
		instructions:  make([]*Instruction, 0),
		poolManager:   poolManager,
		taskResults:   make(map[string][]WorkerResult),
		resultsChan:   make(chan TaskResult, 100),
		consensusChan: make(chan *Instruction, 10),
		retryHistory:  make(map[string]map[string]bool),
		maxRetries:    3, // Maximum number of retry attempts
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
	qm.mu.Unlock()

	// Start processing the instruction
	go qm.processInstruction(instruction)
	return nil
}

// processInstruction handles the execution of a single instruction
func (qm *QueueManager) processInstruction(instruction *Instruction) {
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

		success := qm.tryProcessInstruction(instruction, retryCount)
		if success {
			return
		}

		retryCount++
		if retryCount <= qm.maxRetries {
			// Scale up workers if needed before next retry
			currentWorkers := len(qm.supervisor.workers)
			if currentWorkers < qm.supervisor.maxWorkers {
				newWorkerCount := min(currentWorkers+2, qm.supervisor.maxWorkers)
				if err := qm.supervisor.scaleWorkers(context.Background(), newWorkerCount); err != nil {
					log.Printf("[QueueManager] Failed to scale workers: %v", err)
				} else {
					log.Printf("[QueueManager] Scaled workers from %d to %d for retry", currentWorkers, newWorkerCount)
				}
			}
			time.Sleep(time.Second * 2) // Wait before retry
		}
	}

	log.Printf("[QueueManager] Failed to reach consensus for instruction %s after %d retries", instruction.TaskID, qm.maxRetries)
	qm.resultsChan <- TaskResult{Error: fmt.Errorf("failed to reach consensus after %d retries", qm.maxRetries)}
}

// tryProcessInstruction attempts to process an instruction once
func (qm *QueueManager) tryProcessInstruction(instruction *Instruction, retryCount int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), instruction.Consensus.TimeoutDuration)
	defer cancel()

	// Get unused workers with exponential backoff
	var workers []*WorkerState
	maxAttempts := 10
	baseDelay := time.Second
	for attempt := 0; attempt < maxAttempts; attempt++ {
		availableWorkers, getErr := qm.poolManager.GetAvailableWorkers(instruction.WorkerCount)
		if getErr != nil {
			delay := time.Duration(1<<uint(attempt)) * baseDelay
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
			log.Printf("[QueueManager] Attempt %d: Failed to get workers: %v, retrying in %v...",
				attempt+1, getErr, delay)
			time.Sleep(delay)
			continue
		}

		// Filter out previously used workers
		workers = make([]*WorkerState, 0)
		qm.mu.RLock()
		usedWorkers := qm.retryHistory[instruction.TaskID]
		qm.mu.RUnlock()

		for _, w := range availableWorkers {
			if !usedWorkers[w.ID] {
				workers = append(workers, w)
				if len(workers) >= instruction.WorkerCount {
					break
				}
			}
		}

		if len(workers) >= instruction.WorkerCount {
			break
		}

		delay := time.Duration(1<<uint(attempt)) * baseDelay
		if delay > 10*time.Second {
			delay = 10 * time.Second
		}
		log.Printf("[QueueManager] Not enough unused workers (have %d, need %d), retrying in %v...",
			len(workers), instruction.WorkerCount, delay)
		time.Sleep(delay)
	}

	if len(workers) < instruction.WorkerCount {
		if currentWorkers := len(qm.supervisor.workers); currentWorkers < qm.supervisor.maxWorkers {
			newWorkerCount := min(currentWorkers+2, qm.supervisor.maxWorkers)
			if err := qm.supervisor.scaleWorkers(ctx, newWorkerCount); err != nil {
				log.Printf("[QueueManager] Failed to scale workers: %v", err)
			} else {
				log.Printf("[QueueManager] Scaled workers from %d to %d for retry", currentWorkers, newWorkerCount)
				// Give workers time to initialize
				time.Sleep(time.Second * 2)
				return false
			}
		}
		return false
	}

	// Mark selected workers as used
	qm.mu.Lock()
	for _, w := range workers {
		qm.retryHistory[instruction.TaskID][w.ID] = true
		qm.poolManager.UpdateWorkerStatus(w.ID, "busy")
	}
	qm.mu.Unlock()

	// Create a WaitGroup for worker results
	var wg sync.WaitGroup
	wg.Add(len(workers))

	// Process task with each worker
	for _, worker := range workers {
		go func(w *WorkerState) {
			defer wg.Done()
			defer func() {
				qm.poolManager.UpdateWorkerStatus(w.ID, "available")
			}()

			result, err := qm.processTaskWithWorker(ctx, instruction, w)
			if err != nil {
				log.Printf("[QueueManager] Worker %s failed to process task: %v", w.ID, err)
				return
			}

			qm.mu.Lock()
			qm.taskResults[instruction.TaskID] = append(qm.taskResults[instruction.TaskID], WorkerResult{
				WorkerID:    w.ID,
				Response:    &WorkerResponse{},
				VotingPower: w.VotingPower,
				Timestamp:   time.Now(),
			})

			var response WorkerResponse
			if err := json.Unmarshal([]byte(result), &response); err != nil {
				log.Printf("[QueueManager] Worker %s failed to parse response: %v", w.ID, err)
				qm.mu.Unlock()
				return
			}

			qm.taskResults[instruction.TaskID][len(qm.taskResults[instruction.TaskID])-1].Response = &response
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
		return false
	case <-done:
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
		return "", fmt.Errorf("worker index out of range: %d", workerIndex)
	}
	workerConn := qm.supervisor.workers[workerIndex]
	qm.supervisor.mu.RUnlock()

	if workerConn.client == nil {
		return "", fmt.Errorf("worker %s not connected", worker.ID)
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
			return resp.Result, nil
		case pb.WorkerStatus_FAILED:
			return "", fmt.Errorf(resp.Error)
		case pb.WorkerStatus_PROCESSING:
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

// checkConsensus determines if workers have reached consensus on a task
func (qm *QueueManager) checkConsensus(instruction *Instruction) (bool, string) {
	qm.mu.RLock()
	results := qm.taskResults[instruction.TaskID]
	qm.mu.RUnlock()

	if len(results) == 0 {
		return false, ""
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
		if result.Response.Metadata != nil && strings.Contains(result.Response.Metadata["error"], "Rate limit reached") {
			rateLimitedCount++
			continue
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
		consensusResponse := &WorkerResponse{
			Decision:   bestResult,
			Confidence: bestGroup.confidence,
			Category:   bestGroup.responses[0].Response.Category,
			Reasoning: fmt.Sprintf("Consensus reached with %.2f%% agreement among %d workers (lead: %.2f%%)",
				highestVotes*100, len(bestGroup.responses), voteDifference*100),
			Metadata: map[string]string{
				"consensus_strategy":  string(instruction.Consensus.MatchStrategy),
				"worker_count":        fmt.Sprintf("%d", len(results)),
				"agreeing_workers":    fmt.Sprintf("%d", len(bestGroup.responses)),
				"agreement_threshold": fmt.Sprintf("%.2f", adjustedMinAgreement),
				"actual_agreement":    fmt.Sprintf("%.2f", highestVotes),
				"vote_difference":     fmt.Sprintf("%.2f", voteDifference),
				"total_groups":        fmt.Sprintf("%d", len(groups)),
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

		resultBytes, err := json.Marshal(consensusResponse)
		if err != nil {
			log.Printf("[QueueManager] Failed to marshal consensus response: %v", err)
			return false, ""
		}

		return true, string(resultBytes)
	}

	return false, ""
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
