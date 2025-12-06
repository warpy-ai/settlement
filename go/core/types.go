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

// WorkerResponse represents a structured response from a worker
type WorkerResponse struct {
	// Core response fields that must match for consensus
	Decision   string  `json:"decision"`   // The main decision/answer
	Confidence float64 `json:"confidence"` // Worker's confidence in the result (0-1)
	Category   string  `json:"category"`   // Type of task (translation, analysis, calculation, etc.)

	// Supporting information that doesn't need to match for consensus
	Reasoning       string                 `json:"reasoning"`                  // Explanation of how the decision was reached
	Metadata        map[string]interface{} `json:"metadata"`                   // Additional task-specific metadata (can contain strings, numbers, etc.)
	Alternatives    []string               `json:"alternatives"`               // Alternative answers considered
	MergedReasoning *MergedReasoning       `json:"merged_reasoning,omitempty"` // Synthesized reasoning from agreeable workers
}

// ReasoningContribution represents an attributed piece of merged reasoning
type ReasoningContribution struct {
	WorkerID   string  `json:"worker_id"`  // Source worker identifier
	Text       string  `json:"text"`       // The reasoning excerpt
	Confidence float64 `json:"confidence"` // Worker's confidence when making this contribution
	Order      int     `json:"order"`      // Position in the merged narrative (1-based)
}

// MergedReasoning contains synthesized reasoning from agreeable workers
type MergedReasoning struct {
	Summary                string                  `json:"summary"`                          // Unified narrative summary
	Contributions          []ReasoningContribution `json:"contributions"`                    // Attributed pieces from each worker
	WorkerCount            int                     `json:"worker_count"`                     // Number of workers that contributed
	SynthesisType          string                  `json:"synthesis_type"`                   // "ai" or "algorithmic"
	ConversationalResponse string                  `json:"conversational_response,omitempty"` // Natural chat-friendly response
}

// ConsensusStrategy defines how to compare worker responses
type ConsensusStrategy string

const (
	ExactMatch    ConsensusStrategy = "exact_match"    // Responses must match exactly
	SemanticMatch ConsensusStrategy = "semantic_match" // Responses are compared semantically
	NumericMatch  ConsensusStrategy = "numeric_match"  // Numeric values within tolerance
	MergeMatch    ConsensusStrategy = "merge_match"    // Merge all responses for subjective questions
)

// ConsensusConfig defines how consensus should be reached
type ConsensusConfig struct {
	MinimumAgreement       float64           // Minimum percentage needed for consensus
	TimeoutDuration        time.Duration
	VotingStrategy         string            // e.g., "majority", "weighted"
	MatchStrategy          ConsensusStrategy // How to compare responses
	NumericTolerance       float64           // For numeric comparisons, the acceptable difference
	ExtractMergedReasoning bool              // Whether to extract and merge reasoning from agreeable workers
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

// WorkerResult stores individual worker responses and their voting power
type WorkerResult struct {
	WorkerID    string
	Response    *WorkerResponse
	VotingPower float64
	Timestamp   time.Time
}
