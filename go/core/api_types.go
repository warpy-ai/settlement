package core

import (
	"encoding/json"
	"fmt"
	"time"
)

// Custom duration type that can be unmarshaled from a string
type Duration time.Duration

func (d Duration) ToDuration() time.Duration {
	return time.Duration(d)
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		*d = Duration(time.Duration(value))
		return nil
	case string:
		tmp, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		*d = Duration(tmp)
		return nil
	default:
		return fmt.Errorf("invalid duration type %T", v)
	}
}

// TaskSubmission represents a task submission request
type TaskSubmission struct {
	Content     string           `json:"content"`      // The actual task content
	Rules       *TaskRules       `json:"rules"`        // Optional rules for task processing
	Constraints *TaskConstraints `json:"constraints"`   // Optional processing constraints
	LLMProvider *string          `json:"llm_provider"` // Optional LLM provider override: "openai", "anthropic", "google", "cohere", "mistral"
}

// TaskRules defines specific rules for task processing
type TaskRules struct {
	MinimumAgreement *float64           `json:"minimum_agreement,omitempty"` // Override default agreement threshold
	MatchStrategy    *ConsensusStrategy `json:"match_strategy,omitempty"`    // Override match strategy
	NumericTolerance *float64           `json:"numeric_tolerance,omitempty"` // For numeric comparisons
	Category         *string            `json:"category,omitempty"`          // Task category hint
}

// TaskConstraints defines processing constraints
type TaskConstraints struct {
	WorkerCount    *int      `json:"worker_count,omitempty"`     // Override number of workers
	Timeout        *Duration `json:"timeout,omitempty"`          // Override default timeout
	Priority       *int      `json:"priority,omitempty"`         // Task priority (1-5)
	RetryAttempts  *int      `json:"retry_attempts,omitempty"`   // Maximum retry attempts
	RequireAllPass *bool     `json:"require_all_pass,omitempty"` // Require all workers to succeed
}

// TaskResponse represents the API response for a task submission
type TaskResponse struct {
	TaskID      string    `json:"task_id"`          // Unique task identifier
	Status      string    `json:"status"`           // Current task status
	Result      string    `json:"result,omitempty"` // Final result if completed
	Progress    float64   `json:"progress"`         // Progress percentage
	WorkerCount int       `json:"worker_count"`     // Number of workers assigned
	StartedAt   time.Time `json:"started_at"`       // When the task started
	UpdatedAt   time.Time `json:"updated_at"`       // Last status update
	Error       string    `json:"error,omitempty"`  // Error message if failed
	Metadata    struct {
		ConsensusDetails map[string]interface{} `json:"consensus_details,omitempty"`
		WorkerStatuses   []WorkerStatus         `json:"worker_statuses,omitempty"`
		RetryCount       int                    `json:"retry_count"`
	} `json:"metadata"`
}

// WorkerStatus represents the status of a worker processing a task
type WorkerStatus struct {
	WorkerID  string    `json:"worker_id"`
	Status    string    `json:"status"`     // "waiting", "processing", "completed", "failed"
	Progress  float64   `json:"progress"`   // 0.0 to 1.0
	Reasoning string    `json:"reasoning,omitempty"` // Worker's reasoning/thinking process
	Decision  string    `json:"decision,omitempty"`   // Worker's decision/answer
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskStatusResponse represents the response for a task status query
type TaskStatusResponse struct {
	TaskID    string                 `json:"task_id"`
	Status    string                 `json:"status"`
	Progress  float64                `json:"progress"`
	Result    string                 `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// BatchTaskSubmission represents a batch of tasks to be processed
type BatchTaskSubmission struct {
	Tasks       []TaskSubmission `json:"tasks"`
	CommonRules *TaskRules       `json:"common_rules,omitempty"`        // Rules applied to all tasks
	Constraints *TaskConstraints `json:"constraints,omitempty"`         // Constraints applied to all tasks
	BatchID     string           `json:"batch_id,omitempty"`            // Optional client-provided batch ID
	Parallel    *bool            `json:"process_in_parallel,omitempty"` // Process tasks in parallel if true
}

// BatchTaskResponse represents the response for a batch submission
type BatchTaskResponse struct {
	BatchID     string         `json:"batch_id"`
	TaskIDs     []string       `json:"task_ids"`
	Status      string         `json:"status"`
	TaskResults []TaskResponse `json:"task_results,omitempty"`
	Error       string         `json:"error,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// SupervisorStats represents statistics about the supervisor's operation
type SupervisorStats struct {
	ActiveWorkers    int     `json:"active_workers"`
	TotalWorkers     int     `json:"total_workers"`
	PendingTasks     int     `json:"pending_tasks"`
	CompletedTasks   int     `json:"completed_tasks"`
	FailedTasks      int     `json:"failed_tasks"`
	AverageLoadTime  float64 `json:"average_load_time"`
	SystemLoad       float64 `json:"system_load"`
	SuccessRate      float64 `json:"success_rate"`
	UptimeSeconds    int64   `json:"uptime_seconds"`
	LastScalingEvent string  `json:"last_scaling_event"`
}
