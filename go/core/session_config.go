package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// WorkerConfig represents configuration for a single worker
type WorkerConfig struct {
	SystemPrompt string  `json:"system_prompt,omitempty"` // Custom system prompt, "auto" for default
	Model        string  `json:"model,omitempty"`         // Specific model (e.g., "gpt-4o", "claude-sonnet-4-5-20250929"), "auto" for random
	Provider     string  `json:"provider,omitempty"`      // Provider (e.g., "openai", "anthropic"), "auto" for random
	Temperature  float64 `json:"temperature,omitempty"`   // Model temperature (0.0-2.0)
	MaxTokens    int     `json:"max_tokens,omitempty"`    // Maximum tokens for response
}

// SessionConfig represents the full configuration for a session
type SessionConfig struct {
	Token           string                 `json:"token"`                      // Session token
	WorkerCount     int                    `json:"worker_count"`               // Number of workers (must be odd)
	Workers         map[int]*WorkerConfig  `json:"workers"`                    // Per-worker configuration (index -> config)
	DefaultWorker   *WorkerConfig          `json:"default_worker,omitempty"`   // Default config for workers not specified in Workers
	AllowOverride   bool                   `json:"allow_override"`             // Whether requests can override model configs
	ConsensusRules  *SessionConsensusRules `json:"consensus_rules,omitempty"`  // Default consensus rules
	CreatedAt       time.Time              `json:"created_at"`
	ExpiresAt       time.Time              `json:"expires_at,omitempty"`       // Session expiration time
	Metadata        map[string]interface{} `json:"metadata,omitempty"`         // Custom metadata
}

// SessionConsensusRules defines default consensus behavior for a session
type SessionConsensusRules struct {
	MinimumAgreement float64            `json:"minimum_agreement,omitempty"` // Default agreement threshold (0.5-1.0)
	MatchStrategy    *ConsensusStrategy `json:"match_strategy,omitempty"`    // Default match strategy
	NumericTolerance float64            `json:"numeric_tolerance,omitempty"` // For numeric comparisons
	Timeout          *Duration          `json:"timeout,omitempty"`           // Default timeout per task
	RetryAttempts    int                `json:"retry_attempts,omitempty"`    // Max retry attempts
}

// SessionInitRequest represents the request body for /api/v1/init
type SessionInitRequest struct {
	WorkerCount    int                    `json:"worker_count"`               // Number of workers (3-15, must be odd)
	Workers        map[int]*WorkerConfig  `json:"workers,omitempty"`          // Per-worker configuration
	DefaultWorker  *WorkerConfig          `json:"default_worker,omitempty"`   // Default config for unspecified workers
	AllowOverride  bool                   `json:"allow_override"`             // Allow per-request model overrides
	ConsensusRules *SessionConsensusRules `json:"consensus_rules,omitempty"`  // Default consensus rules
	TTLSeconds     int                    `json:"ttl_seconds,omitempty"`      // Session TTL in seconds (default: 3600)
	Metadata       map[string]interface{} `json:"metadata,omitempty"`         // Custom metadata
}

// SessionInitResponse represents the response from /api/v1/init
type SessionInitResponse struct {
	Token         string            `json:"token"`                    // Session token to use in /submit
	WorkerCount   int               `json:"worker_count"`             // Configured worker count
	WorkerConfigs map[int]string    `json:"worker_configs"`           // Summary of worker configs (index -> "provider/model")
	AllowOverride bool              `json:"allow_override"`           // Whether overrides are allowed
	ExpiresAt     time.Time         `json:"expires_at"`               // Session expiration time
	CreatedAt     time.Time         `json:"created_at"`               // Session creation time
}

// SessionSubmitRequest extends TaskSubmission with session token
type SessionSubmitRequest struct {
	Token            string           `json:"token"`                       // Session token from /init
	Content          string           `json:"content"`                     // Task content
	Rules            *TaskRules       `json:"rules,omitempty"`             // Override session rules
	Constraints      *TaskConstraints `json:"constraints,omitempty"`       // Override session constraints
	ModelPreferences map[int]string   `json:"model_preferences,omitempty"` // Override worker models (if allowed)
}

// SessionManager manages active sessions
type SessionManager struct {
	sessions map[string]*SessionConfig
	mu       sync.RWMutex
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*SessionConfig),
	}
}

// GenerateToken creates a secure random token
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate token: %v", err)
	}
	return hex.EncodeToString(bytes), nil
}

// CreateSession creates a new session from an init request
func (sm *SessionManager) CreateSession(req *SessionInitRequest) (*SessionConfig, error) {
	// Validate worker count
	if req.WorkerCount < 3 || req.WorkerCount > 15 {
		return nil, fmt.Errorf("worker_count must be between 3 and 15")
	}
	if req.WorkerCount%2 == 0 {
		return nil, fmt.Errorf("worker_count must be an odd number")
	}

	// Generate token
	token, err := GenerateToken()
	if err != nil {
		return nil, err
	}

	// Set default TTL
	ttl := req.TTLSeconds
	if ttl <= 0 {
		ttl = 3600 // Default 1 hour
	}

	now := time.Now()
	session := &SessionConfig{
		Token:          token,
		WorkerCount:    req.WorkerCount,
		Workers:        req.Workers,
		DefaultWorker:  req.DefaultWorker,
		AllowOverride:  req.AllowOverride,
		ConsensusRules: req.ConsensusRules,
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Duration(ttl) * time.Second),
		Metadata:       req.Metadata,
	}

	// Initialize workers map if nil
	if session.Workers == nil {
		session.Workers = make(map[int]*WorkerConfig)
	}

	sm.mu.Lock()
	sm.sessions[token] = session
	sm.mu.Unlock()

	return session, nil
}

// GetSession retrieves a session by token
func (sm *SessionManager) GetSession(token string) (*SessionConfig, error) {
	sm.mu.RLock()
	session, exists := sm.sessions[token]
	sm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("session not found")
	}

	// Check expiration
	if time.Now().After(session.ExpiresAt) {
		sm.DeleteSession(token)
		return nil, fmt.Errorf("session expired")
	}

	return session, nil
}

// DeleteSession removes a session
func (sm *SessionManager) DeleteSession(token string) {
	sm.mu.Lock()
	delete(sm.sessions, token)
	sm.mu.Unlock()
}

// CleanupExpiredSessions removes expired sessions
func (sm *SessionManager) CleanupExpiredSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for token, session := range sm.sessions {
		if now.After(session.ExpiresAt) {
			delete(sm.sessions, token)
		}
	}
}

// StartCleanupRoutine starts a background goroutine to clean up expired sessions
func (sm *SessionManager) StartCleanupRoutine() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			sm.CleanupExpiredSessions()
		}
	}()
}

// GetWorkerConfig returns the configuration for a specific worker index
// Falls back to default worker config if not specified
func (sc *SessionConfig) GetWorkerConfig(index int) *WorkerConfig {
	if config, exists := sc.Workers[index]; exists {
		return config
	}
	if sc.DefaultWorker != nil {
		return sc.DefaultWorker
	}
	// Return empty config (will use system defaults)
	return &WorkerConfig{}
}

// IsAutoModel returns true if the model should be auto-selected
func (wc *WorkerConfig) IsAutoModel() bool {
	return wc.Model == "" || wc.Model == "auto"
}

// IsAutoProvider returns true if the provider should be auto-selected
func (wc *WorkerConfig) IsAutoProvider() bool {
	return wc.Provider == "" || wc.Provider == "auto"
}

// IsAutoSystemPrompt returns true if the system prompt should be auto-generated
func (wc *WorkerConfig) IsAutoSystemPrompt() bool {
	return wc.SystemPrompt == "" || wc.SystemPrompt == "auto"
}
