package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"settlement-core/llm-client"
)

type TaskRequirements struct {
	WorkerCount      int               `json:"worker_count"`
	MinimumAgreement float64           `json:"minimum_agreement"`
	Complexity       string            `json:"complexity"` // "low", "medium", "high"
	Priority         int               `json:"priority"`   // 1-5, where 5 is highest
	TimeoutSeconds   int               `json:"timeout_seconds"`
	MatchStrategy    ConsensusStrategy `json:"match_strategy"`
	NumericTolerance float64           `json:"numeric_tolerance"`
}

// cleanJSONResponse removes markdown code blocks from the response
func cleanJSONResponse(content string) string {
	content = strings.TrimSpace(content)
	
	// Remove markdown code blocks (```json ... ``` or ``` ... ```)
	if strings.HasPrefix(content, "```") {
		// Find the first newline after ``` (or ```json, ```json, etc.)
		lines := strings.Split(content, "\n")
		if len(lines) > 0 {
			// Skip the first line (which contains ``` or ```json)
			if len(lines) > 1 {
				content = strings.Join(lines[1:], "\n")
			} else {
				// Single line, just remove the ```
				content = strings.TrimPrefix(content, "```")
			}
		}
		
		// Remove trailing ``` (might be on its own line or at end of last line)
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	
	return strings.TrimSpace(content)
}

// AnalyzeTask uses LLM to determine task requirements
func AnalyzeTask(ctx context.Context, task string, apiKey string) (*TaskRequirements, error) {
	// Get provider from environment or default to OpenAI
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "openai"
	}

	// Create LLM client
	llmClient, err := llmclient.NewLLMClient(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client: %v", err)
	}

	systemPrompt := `You are a task analyzer that determines the requirements for processing a task in a multi-agent system.
You must respond with ONLY a JSON object (no other text) containing these fields:
{
    "worker_count": <odd number between 3-7>,
    "minimum_agreement": <float between 0.5-1.0>,
    "complexity": <"low", "medium", or "high">,
    "priority": <integer 1-5, where 5 is highest>,
    "timeout_seconds": <integer between 30-300>,
    "match_strategy": <"exact_match", "semantic_match", "numeric_match", or "merge_match">,
    "numeric_tolerance": <float, e.g., 0.01 for calculations>
}

Choose match_strategy based on task type:
- exact_match: For tasks requiring exact matches (e.g., simple translations)
- semantic_match: For tasks with potential wording variations (e.g., analysis, complex translations)
- numeric_match: For calculations and numeric results
- merge_match: For subjective questions with multiple valid answers (e.g., "who is the best...", "what is your opinion on...", "recommend...")

Set numeric_tolerance based on precision requirements:
- 0.01 for financial calculations
- 0.1 for general numeric tasks
- 1.0 for rough estimates

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
    "timeout_seconds": 60,
    "match_strategy": "semantic_match",
    "numeric_tolerance": 0.1
}`

	userMessage := fmt.Sprintf("Analyze this task and respond with ONLY the JSON object: %s", task)

	resp, err := llmClient.Call(ctx, systemPrompt, userMessage, apiKey)
	if err != nil {
		return nil, fmt.Errorf("LLM API error: %v", err)
	}

	// Clean the response content (remove markdown code blocks if present)
	cleanedContent := cleanJSONResponse(resp)

	var requirements TaskRequirements
	if err := json.Unmarshal([]byte(cleanedContent), &requirements); err != nil {
		return nil, fmt.Errorf("failed to parse requirements: %v\nResponse: %s", err, resp)
	}

	// Validate requirements
	if err := validateRequirements(&requirements); err != nil {
		return nil, fmt.Errorf("invalid requirements: %v\nResponse: %s", err, resp)
	}

	log.Printf("[TaskAnalyzer] Task analyzed: workers=%d, agreement=%.2f, complexity=%s, priority=%d, timeout=%ds, strategy=%s",
		requirements.WorkerCount,
		requirements.MinimumAgreement,
		requirements.Complexity,
		requirements.Priority,
		requirements.TimeoutSeconds,
		requirements.MatchStrategy)

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

	switch req.MatchStrategy {
	case ExactMatch, SemanticMatch, NumericMatch, MergeMatch:
		// Valid strategy
	default:
		return fmt.Errorf("invalid match strategy: %s", req.MatchStrategy)
	}

	if req.MatchStrategy == NumericMatch && req.NumericTolerance <= 0 {
		return fmt.Errorf("numeric match strategy requires positive tolerance value")
	}

	return nil
}
