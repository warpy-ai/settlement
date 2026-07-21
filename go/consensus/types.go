// Package consensus implements Settlement's provider-agnostic vote tallying:
// weighted voting over structured responses with exact, semantic, numeric, and
// merge match strategies. It has no dependency on the worker pool, gRPC, or any
// LLM SDK — the optional AI-synthesis hook is injected via the Synthesizer
// interface, and a nil Synthesizer selects fully offline algorithmic fallbacks.
//
// The code was extracted verbatim from core/queue_manager.go so that the HTTP
// service and the local settle CLI share one consensus engine.
package consensus

import "time"

// Response represents a structured response from a voter (worker or persona).
type Response struct {
	// Core response fields that must match for consensus
	Decision   string  `json:"decision"`   // The main decision/answer
	Confidence float64 `json:"confidence"` // Voter's confidence in the result (0-1)
	Category   string  `json:"category"`   // Type of task (translation, analysis, calculation, etc.)

	// Supporting information that doesn't need to match for consensus
	Reasoning       string                 `json:"reasoning"`                  // Explanation of how the decision was reached
	Metadata        map[string]interface{} `json:"metadata"`                   // Additional task-specific metadata
	Alternatives    []string               `json:"alternatives"`               // Alternative answers considered
	MergedReasoning *MergedReasoning       `json:"merged_reasoning,omitempty"` // Synthesized reasoning from agreeable voters
}

// Result stores an individual voter's response and voting power.
type Result struct {
	WorkerID    string
	Response    *Response
	VotingPower float64
	Timestamp   time.Time
}

// ReasoningContribution represents an attributed piece of merged reasoning.
type ReasoningContribution struct {
	WorkerID   string  `json:"worker_id"`  // Source voter identifier
	Text       string  `json:"text"`       // The reasoning excerpt
	Confidence float64 `json:"confidence"` // Voter's confidence when making this contribution
	Order      int     `json:"order"`      // Position in the merged narrative (1-based)
}

// MergedReasoning contains synthesized reasoning from agreeable voters.
type MergedReasoning struct {
	Summary                string                  `json:"summary"`                           // Unified narrative summary
	Contributions          []ReasoningContribution `json:"contributions"`                     // Attributed pieces from each voter
	WorkerCount            int                     `json:"worker_count"`                      // Number of voters that contributed
	SynthesisType          string                  `json:"synthesis_type"`                    // "ai" or "algorithmic"
	ConversationalResponse string                  `json:"conversational_response,omitempty"` // Natural chat-friendly response
}

// Strategy defines how to compare voter responses.
type Strategy string

const (
	ExactMatch    Strategy = "exact_match"    // Responses must match exactly
	SemanticMatch Strategy = "semantic_match" // Responses are compared semantically
	NumericMatch  Strategy = "numeric_match"  // Numeric values within tolerance
	MergeMatch    Strategy = "merge_match"    // Merge all responses for subjective questions
)

// Config defines how consensus should be reached.
type Config struct {
	MinimumAgreement       float64 // Minimum percentage needed for consensus
	TimeoutDuration        time.Duration
	VotingStrategy         string   // e.g., "majority", "weighted"
	MatchStrategy          Strategy // How to compare responses
	NumericTolerance       float64  // For numeric comparisons, the acceptable difference
	ExtractMergedReasoning bool     // Whether to extract and merge reasoning from agreeable voters
}
