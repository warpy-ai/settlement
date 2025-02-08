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
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.workers[workerID] = &WorkerState{
		ID:            workerID,
		Status:        "available",
		VotingPower:   votingPower,
		LastHeartbeat: time.Now(),
	}
	log.Printf("Worker %s registered with voting power %.2f", workerID, votingPower)
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
	for _, worker := range pm.workers {
		if worker.Status == "available" {
			available = append(available, worker)
		}
	}

	if len(available) < count {
		return nil, fmt.Errorf("not enough available workers. Need %d, have %d", count, len(available))
	}

	// Return the requested number of workers
	return available[:count], nil
}

// UpdateWorkerStatus updates the status of a worker
func (pm *PoolManager) UpdateWorkerStatus(workerID string, status string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	worker, exists := pm.workers[workerID]
	if !exists {
		return fmt.Errorf("worker %s not found", workerID)
	}

	worker.Status = status
	worker.LastHeartbeat = time.Now()
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

// monitorWorkerHealth periodically checks worker health and updates status
func (pm *PoolManager) monitorWorkerHealth() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pm.mu.Lock()
		now := time.Now()
		for id, worker := range pm.workers {
			if now.Sub(worker.LastHeartbeat) > 30*time.Second {
				worker.Status = "offline"
				log.Printf("Worker %s marked as offline due to inactivity", id)
			}
		}
		pm.mu.Unlock()
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
