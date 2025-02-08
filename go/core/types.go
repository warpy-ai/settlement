package core

import (
	"sync"
	"time"
)

// Instruction represents a task with its requirements and results
type Instruction struct {
	TaskID      string
	Content     string
	WorkerCount int // Must be odd number as per settlement requirements
	Results     []WorkerResult
	Consensus   ConsensusConfig
}

// WorkerResult stores individual worker responses and their voting power
type WorkerResult struct {
	WorkerID    string
	Result      string
	VotingPower float64
	Timestamp   time.Time
}

// ConsensusConfig defines how consensus should be reached
type ConsensusConfig struct {
	MinimumAgreement float64 // Minimum percentage needed for consensus
	TimeoutDuration  time.Duration
	VotingStrategy   string // e.g., "majority", "weighted"
}

// WorkerPool manages available workers and their states
type WorkerPool struct {
	mu      sync.RWMutex
	workers map[string]*WorkerState
}

// WorkerState tracks individual worker status
type WorkerState struct {
	ID            string
	Status        string // "available", "busy", "offline"
	CurrentTaskID string
	VotingPower   float64
	LastHeartbeat time.Time
}

// TaskQueue manages pending instructions
type TaskQueue struct {
	mu           sync.Mutex
	instructions []*Instruction
}

// TaskResult represents the result of a task execution
type TaskResult struct {
	Result string
	Error  error
}
