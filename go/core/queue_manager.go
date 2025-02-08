package core

import (
	"context"
	"fmt"
	"log"
	pb "settlement-core/proto/gen/proto"
	"sync"
	"time"
)

// QueueManager handles the instruction queue and task distribution
type QueueManager struct {
	mu            sync.RWMutex
	instructions  []*Instruction
	poolManager   *PoolManager
	taskResults   map[string][]WorkerResult
	resultsChan   chan TaskResult
	consensusChan chan *Instruction
	supervisor    *Supervisor // Add reference to supervisor
}

// NewQueueManager creates a new queue manager instance
func NewQueueManager(poolManager *PoolManager) *QueueManager {
	return &QueueManager{
		instructions:  make([]*Instruction, 0),
		poolManager:   poolManager,
		taskResults:   make(map[string][]WorkerResult),
		resultsChan:   make(chan TaskResult, 100),
		consensusChan: make(chan *Instruction, 10),
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
	ctx, cancel := context.WithTimeout(context.Background(), instruction.Consensus.TimeoutDuration)
	defer cancel()

	// Get available workers
	workers, err := qm.poolManager.GetAvailableWorkers(instruction.WorkerCount)
	if err != nil {
		log.Printf("Failed to get workers for instruction %s: %v", instruction.TaskID, err)
		qm.resultsChan <- TaskResult{Error: err}
		return
	}

	// Create a WaitGroup for worker results
	var wg sync.WaitGroup
	wg.Add(len(workers))

	// Process task with each worker
	for _, worker := range workers {
		go func(w *WorkerState) {
			defer wg.Done()

			// Mark worker as busy
			w.Status = "busy"
			w.CurrentTaskID = instruction.TaskID

			// Process the task
			result, err := qm.processTaskWithWorker(ctx, instruction, w)
			if err != nil {
				log.Printf("Worker %s failed to process task: %v", w.ID, err)
				return
			}

			// Store the result
			qm.mu.Lock()
			qm.taskResults[instruction.TaskID] = append(qm.taskResults[instruction.TaskID], WorkerResult{
				WorkerID:    w.ID,
				Result:      result,
				VotingPower: w.VotingPower,
				Timestamp:   time.Now(),
			})
			qm.mu.Unlock()

			// Mark worker as available
			w.Status = "available"
			w.CurrentTaskID = ""
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
		log.Printf("Instruction %s timed out", instruction.TaskID)
		qm.resultsChan <- TaskResult{Error: fmt.Errorf("instruction timed out")}
	case <-done:
		// Check consensus
		consensus, result := qm.checkConsensus(instruction)
		if consensus {
			qm.resultsChan <- TaskResult{Result: result}
		} else {
			qm.resultsChan <- TaskResult{Error: fmt.Errorf("failed to reach consensus")}
		}
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

// checkConsensus determines if workers have reached consensus on a task
func (qm *QueueManager) checkConsensus(instruction *Instruction) (bool, string) {
	qm.mu.RLock()
	results := qm.taskResults[instruction.TaskID]
	qm.mu.RUnlock()

	if len(results) == 0 {
		return false, ""
	}

	// Count occurrences of each result
	resultCounts := make(map[string]float64)
	totalVotingPower := 0.0

	for _, result := range results {
		resultCounts[result.Result] += result.VotingPower
		totalVotingPower += result.VotingPower
	}

	// Find the result with the highest voting power
	var bestResult string
	var highestVotes float64

	for result, votes := range resultCounts {
		percentage := votes / totalVotingPower
		if percentage > highestVotes {
			highestVotes = percentage
			bestResult = result
		}
	}

	// Check if the best result meets the minimum agreement threshold
	if highestVotes >= instruction.Consensus.MinimumAgreement {
		return true, bestResult
	}

	return false, ""
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
