package core

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// PoolManager handles worker pool management and health monitoring
type PoolManager struct {
	mu      sync.RWMutex
	workers map[string]*WorkerState
}

// NewPoolManager creates a new pool manager instance
func NewPoolManager() *PoolManager {
	pm := &PoolManager{
		workers: make(map[string]*WorkerState),
	}
	go pm.monitorWorkerHealth()
	return pm
}

// RegisterWorker adds a new worker to the pool
func (pm *PoolManager) RegisterWorker(workerID string, votingPower float64) {
	pm.RegisterWorkerWithModel(workerID, votingPower, "", "")
}

// RegisterWorkerWithModel adds a new worker to the pool with provider/model info
func (pm *PoolManager) RegisterWorkerWithModel(workerID string, votingPower float64, provider, model string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.workers[workerID] = &WorkerState{
		ID:            workerID,
		Status:        "available",
		VotingPower:   votingPower,
		LastHeartbeat: time.Now(),
		Provider:      provider,
		Model:         model,
	}
	log.Printf("Worker %s registered with voting power %.2f, provider: %s, model: %s", workerID, votingPower, provider, model)
}

// UnregisterWorker removes a worker from the pool
func (pm *PoolManager) UnregisterWorker(workerID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	delete(pm.workers, workerID)
	log.Printf("Worker %s unregistered", workerID)
}

// GetAvailableWorkers returns a list of available workers
func (pm *PoolManager) GetAvailableWorkers(count int) ([]*WorkerState, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var available []*WorkerState
	var stale []*WorkerState
	var busy []*WorkerState
	var offline []*WorkerState

	now := time.Now()
	for _, worker := range pm.workers {
		timeSinceHeartbeat := now.Sub(worker.LastHeartbeat)
		
		// Categorize workers based on status and heartbeat
		if timeSinceHeartbeat > 30*time.Second {
			stale = append(stale, worker)
		} else if worker.Status == "available" {
			available = append(available, worker)
		} else if worker.Status == "busy" {
			busy = append(busy, worker)
		} else {
			offline = append(offline, worker)
		}
	}

	if len(available) < count {
		// Provide detailed worker pool status
		log.Printf("[PoolManager] Worker pool status - Available: %d, Stale: %d, Busy: %d, Offline: %d (Need: %d)",
			len(available), len(stale), len(busy), len(offline), count)
		
		// If we have stale workers, trigger cleanup
		if len(stale) > 0 {
			go pm.CleanupStaleWorkers()
		}
		
		return nil, fmt.Errorf("not enough available workers. Need %d, have %d (Stale: %d, Busy: %d, Offline: %d)",
			count, len(available), len(stale), len(busy), len(offline))
	}

	// Return the requested number of workers
	return available[:count], nil
}

// CleanupStaleWorkers removes workers that haven't sent heartbeats
func (pm *PoolManager) CleanupStaleWorkers() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	staleWorkers := 0
	releasedWorkers := 0

	for id, worker := range pm.workers {
		timeSinceHeartbeat := now.Sub(worker.LastHeartbeat)
		if timeSinceHeartbeat > 30*time.Second {
			staleWorkers++
			if worker.Status == "busy" {
				worker.Status = "available"
				worker.CurrentTaskID = ""
				releasedWorkers++
				log.Printf("[PoolManager] Force released stale worker %s (no heartbeat for %.0f seconds)",
					id, timeSinceHeartbeat.Seconds())
			}
		}
	}

	if staleWorkers > 0 {
		log.Printf("[PoolManager] Cleanup completed - Found %d stale workers, released %d busy workers",
			staleWorkers, releasedWorkers)
	}
}

// UpdateWorkerStatus updates the status of a worker
func (pm *PoolManager) UpdateWorkerStatus(workerID string, status string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return fmt.Errorf("worker %s not found", workerID)
	}

	// Only update if status actually changes
	if worker.Status != status {
		oldStatus := worker.Status
		worker.Status = status
		worker.LastHeartbeat = time.Now()

		// Clear current task ID when worker becomes available
		if status == "available" {
			worker.CurrentTaskID = ""
		}

		log.Printf("[PoolManager] Worker %s status changed: %s -> %s", workerID, oldStatus, status)
	} else if status == "available" {
		// Just update heartbeat without logging
		worker.LastHeartbeat = time.Now()
	}

	return nil
}

// GetWorkerStatus returns the current status of a worker
func (pm *PoolManager) GetWorkerStatus(workerID string) (string, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return "", fmt.Errorf("worker %s not found", workerID)
	}

	return worker.Status, nil
}

// GetWorkerByID returns the worker state for a given worker ID
func (pm *PoolManager) GetWorkerByID(workerID string) (*WorkerState, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return nil, fmt.Errorf("worker %s not found", workerID)
	}

	return worker, nil
}

// GetWorkerCount returns the total number of workers and number of available workers
func (pm *PoolManager) GetWorkerCount() (total int, available int) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, worker := range pm.workers {
		total++
		if worker.Status == "available" {
			available++
		}
	}

	return total, available
}

// AssignTaskToWorker marks a worker as busy with a specific task
func (pm *PoolManager) AssignTaskToWorker(workerID string, taskID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return fmt.Errorf("worker %s not found", workerID)
	}

	if worker.Status != "available" {
		return fmt.Errorf("worker %s is not available (current status: %s)", workerID, worker.Status)
	}

	worker.Status = "busy"
	worker.CurrentTaskID = taskID
	worker.LastHeartbeat = time.Now()
	log.Printf("[PoolManager] Worker %s assigned task %s", workerID, taskID)
	return nil
}

// ReleaseWorker marks a worker as available and clears its task
func (pm *PoolManager) ReleaseWorker(workerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return fmt.Errorf("worker %s not found", workerID)
	}

	if worker.Status == "busy" {
		worker.Status = "available"
		worker.CurrentTaskID = ""
		worker.LastHeartbeat = time.Now()
		log.Printf("[PoolManager] Worker %s released and now available", workerID)
	}

	return nil
}

// monitorWorkerHealth periodically checks worker health and updates status
func (pm *PoolManager) monitorWorkerHealth() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		pm.mu.RLock()
		now := time.Now()
		staleCount := 0
		for _, worker := range pm.workers {
			if now.Sub(worker.LastHeartbeat) > 30*time.Second {
				staleCount++
			}
		}
		pm.mu.RUnlock()

		// If we have stale workers, trigger cleanup
		if staleCount > 0 {
			pm.CleanupStaleWorkers()
		}
	}
}

// GetWorkersByStatus returns a list of workers filtered by status
func (pm *PoolManager) GetWorkersByStatus(status string) []*WorkerState {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var filtered []*WorkerState
	for _, worker := range pm.workers {
		if worker.Status == status {
			filtered = append(filtered, worker)
		}
	}

	return filtered
}

// GetWorkerLoad returns the current load (busy workers / total workers)
func (pm *PoolManager) GetWorkerLoad() float64 {
	total, available := pm.GetWorkerCount()
	if total == 0 {
		return 0
	}
	return float64(total-available) / float64(total)
}
