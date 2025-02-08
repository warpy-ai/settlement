package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	openai "github.com/sashabaranov/go-openai"
)

type TaskRequirements struct {
	WorkerCount      int     `json:"worker_count"`
	MinimumAgreement float64 `json:"minimum_agreement"`
	Complexity       string  `json:"complexity"` // "low", "medium", "high"
	Priority         int     `json:"priority"`   // 1-5, where 5 is highest
	TimeoutSeconds   int     `json:"timeout_seconds"`
}

// AnalyzeTask uses OpenAI to determine task requirements
func AnalyzeTask(ctx context.Context, task string, apiKey string) (*TaskRequirements, error) {
	client := openai.NewClient(apiKey)

	systemPrompt := `You are a task analyzer that determines the requirements for processing a task in a multi-agent system.
You must respond with ONLY a JSON object (no other text) containing these fields:
{
    "worker_count": <odd number between 3-7>,
    "minimum_agreement": <float between 0.5-1.0>,
    "complexity": <"low", "medium", or "high">,
    "priority": <integer 1-5, where 5 is highest>,
    "timeout_seconds": <integer between 30-300>
}

Base your analysis on:
1. Task complexity and ambiguity
2. Need for accuracy and verification
3. Time sensitivity
4. Potential for divergent results

Example response:
{
    "worker_count": 3,
    "minimum_agreement": 0.66,
    "complexity": "medium",
    "priority": 3,
    "timeout_seconds": 60
}`

	resp, err := client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: openai.GPT4,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: systemPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: fmt.Sprintf("Analyze this task and respond with ONLY the JSON object: %s", task),
				},
			},
			Temperature: 0.1, // Lower temperature for more consistent responses
		},
	)

	if err != nil {
		return nil, fmt.Errorf("OpenAI API error: %v", err)
	}

	var requirements TaskRequirements
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &requirements); err != nil {
		return nil, fmt.Errorf("failed to parse requirements: %v\nResponse: %s", err, resp.Choices[0].Message.Content)
	}

	// Validate requirements
	if err := validateRequirements(&requirements); err != nil {
		return nil, fmt.Errorf("invalid requirements: %v\nResponse: %s", err, resp.Choices[0].Message.Content)
	}

	log.Printf("[TaskAnalyzer] Task analyzed: workers=%d, agreement=%.2f, complexity=%s, priority=%d, timeout=%ds",
		requirements.WorkerCount,
		requirements.MinimumAgreement,
		requirements.Complexity,
		requirements.Priority,
		requirements.TimeoutSeconds)

	return &requirements, nil
}

func validateRequirements(req *TaskRequirements) error {
	if req.WorkerCount < 3 || req.WorkerCount > 7 || req.WorkerCount%2 == 0 {
		return fmt.Errorf("invalid worker count: %d (must be odd number between 3 and 7)", req.WorkerCount)
	}

	if req.MinimumAgreement < 0.5 || req.MinimumAgreement > 1.0 {
		return fmt.Errorf("invalid minimum agreement: %.2f (must be between 0.5 and 1.0)", req.MinimumAgreement)
	}

	if req.Priority < 1 || req.Priority > 5 {
		return fmt.Errorf("invalid priority: %d (must be between 1 and 5)", req.Priority)
	}

	if req.TimeoutSeconds < 30 || req.TimeoutSeconds > 300 {
		return fmt.Errorf("invalid timeout: %d (must be between 30 and 300 seconds)", req.TimeoutSeconds)
	}

	switch req.Complexity {
	case "low", "medium", "high":
		// Valid complexity
	default:
		return fmt.Errorf("invalid complexity: %s (must be low, medium, or high)", req.Complexity)
	}

	return nil
}
