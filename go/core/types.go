package core

import (
	"sync"
	"time"

	"settlement-core/consensus"
)

// Consensus-related types moved to the shared settlement-core/consensus
// package so the HTTP service and the settle CLI use one engine. Aliases keep
// the rest of core (and its JSON contracts) unchanged.
type (
	WorkerResponse        = consensus.Response
	WorkerResult          = consensus.Result
	MergedReasoning       = consensus.MergedReasoning
	ReasoningContribution = consensus.ReasoningContribution
	ConsensusConfig       = consensus.Config
	ConsensusStrategy     = consensus.Strategy
)

const (
	ExactMatch    = consensus.ExactMatch
	SemanticMatch = consensus.SemanticMatch
	NumericMatch  = consensus.NumericMatch
	MergeMatch    = consensus.MergeMatch
)

// Instruction represents a task with its requirements and results
type Instruction struct {
	TaskID              string
	Content             string
	WorkerCount         int // Must be odd number as per settlement requirements
	Results             []WorkerResult
	Consensus           ConsensusConfig
	ModelPreferences    map[int]string // Optional per-position provider preferences (position 0-4 -> provider name)
	WorkerSystemPrompts map[int]string // Optional per-worker system prompts (worker index -> prompt)
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
	Provider      string // LLM provider (openai, anthropic, google, cohere, mistral)
	Model         string // LLM model name
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
