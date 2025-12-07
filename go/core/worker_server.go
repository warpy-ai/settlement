package core

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	llmclient "settlement-core/llm-client"
	pb "settlement-core/proto/gen/proto"
	"strings"
)

// WorkerServer implements the gRPC worker service
type WorkerServer struct {
	pb.UnimplementedWorkerServiceServer
	workerID  int
	llmClient llmclient.LLMClient
	provider  string
	model     string
}

// NewWorkerServer creates a new worker server instance
// Priority: explicit env overrides -> reliable models (if flag) -> random
func NewWorkerServer(id int) *WorkerServer {
	// Env overrides let the supervisor pin provider/model per worker
	providerOverride := strings.TrimSpace(os.Getenv("LLM_PROVIDER"))
	modelOverride := strings.TrimSpace(os.Getenv("LLM_MODEL"))

	useReliableOnly := os.Getenv("USE_RELIABLE_MODELS_ONLY") == "true"

	var config llmclient.ModelConfig
	switch {
	case providerOverride != "":
		// If model not provided, pick a random model for this provider
		if modelOverride == "" {
			models := llmclient.GetModelsForProvider(llmclient.Provider(providerOverride))
			if len(models) > 0 {
				modelOverride = models[rand.Intn(len(models))]
			}
		}
		config = llmclient.ModelConfig{
			Provider: providerOverride,
			Model:    modelOverride,
		}
	case useReliableOnly:
		config = llmclient.GetRandomLLMConfigFromMap(llmclient.GetReliableModels())
	default:
		config = llmclient.GetRandomLLMConfig()
	}

	if config.Model == "" {
		// Last resort: default model for the chosen provider
		models := llmclient.GetModelsForProvider(llmclient.Provider(config.Provider))
		if len(models) > 0 {
			config.Model = models[rand.Intn(len(models))]
		}
	}

	// Create LLM client with the selected model
	client, err := llmclient.NewLLMClientWithModel(config.Provider, config.Model)
	if err != nil {
		log.Printf("[Worker %d] Failed to create LLM client with %s/%s, falling back to OpenAI: %v", id, config.Provider, config.Model, err)
		// Fallback to OpenAI if selection fails
		client, _ = llmclient.NewLLMClient("openai")
		config.Provider = "openai"
		config.Model = "gpt-4o"
	}

	// For Google provider, try to use a model that's more likely to work
	// If the selected model fails, we'll catch it during the first API call
	if config.Provider == "google" {
		// Log which model we're trying
		log.Printf("[Worker %d] Using Google model: %s (if this fails, worker will retry with fallback)", id, config.Model)
	}

	log.Printf("[Worker %d] Initialized with LLM: %s, Model: %s", id, config.Provider, config.Model)

	return &WorkerServer{
		workerID:  id,
		llmClient: client,
		provider:  config.Provider,
		model:     config.Model,
	}
}

// determineTaskCategory analyzes the task content to determine its category
func (s *WorkerServer) determineTaskCategory(task string) string {
	task = strings.ToLower(task)
	switch {
	case strings.Contains(task, "translate"):
		return "translation"
	case strings.Contains(task, "analyze") || strings.Contains(task, "evaluate"):
		return "analysis"
	case strings.Contains(task, "calculate"):
		return "calculation"
	default:
		return "general"
	}
}

// ProcessTask handles incoming task requests
func (s *WorkerServer) ProcessTask(req *pb.TaskRequest, stream pb.WorkerService_ProcessTaskServer) error {
	log.Printf("[Worker %d] Received task: %s", s.workerID, req.Content)

	// Send processing status
	if err := stream.Send(&pb.TaskResponse{
		TaskId: req.TaskId,
		Status: pb.WorkerStatus_PROCESSING,
	}); err != nil {
		return err
	}

	// Determine task category
	category := s.determineTaskCategory(req.Content)

	// Create system prompt based on category
	systemPrompt := fmt.Sprintf(`You are an AI worker processing a %s task. 
Your response must be a JSON object with the following structure:
{
    "decision": "your main answer/decision",
    "confidence": <float between 0-1>,
    "category": "%s",
    "reasoning": "detailed explanation of your thought process",
    "metadata": {
        "key1": "value1",
        "key2": "value2"
    },
    "alternatives": ["alternative1", "alternative2"]
}

IMPORTANT: Be decisive and direct. When asked "who is the best" or similar questions, provide a CLEAR, DEFINITIVE answer. Do not hedge or say "it depends" - make a decision based on the most common criteria (e.g., achievements, statistics, impact). Pick ONE answer.

For translations:
- decision: the translated text
- metadata: source_language, target_language, formality_level
- alternatives: alternative translations

For analysis:
- decision: clear, concise conclusion - be definitive
- metadata: key_factors, data_sources, confidence_factors
- alternatives: alternative viewpoints

For calculations:
- decision: the final calculated value
- metadata: formula_used, units, precision
- alternatives: results with different methods

Respond ONLY with the JSON object, no other text.`, category, category)

	// Get the appropriate API key for the worker's assigned provider
	apiKey := s.getAPIKeyForProvider(req.ApiKey)

	// Process task using the worker's assigned LLM
	result, err := s.llmClient.Call(stream.Context(), systemPrompt, req.Content, apiKey)
	if err != nil {
		errMsg := fmt.Sprintf("Worker %d failed: %v", s.workerID, err)
		stream.Send(&pb.TaskResponse{
			TaskId: req.TaskId,
			Error:  errMsg,
			Status: pb.WorkerStatus_FAILED,
		})
		return err
	}
	log.Printf("[Worker %d] Result: %s", s.workerID, result)

	// Clean the response content (remove markdown code blocks if present)
	cleanedResult := cleanJSONResponse(result)

	// Log the raw response for debugging (truncated)
	if len(cleanedResult) > 500 {
		log.Printf("[Worker %d] Raw response (truncated): %s...", s.workerID, cleanedResult[:500])
	} else {
		log.Printf("[Worker %d] Raw response: %s", s.workerID, cleanedResult)
	}

	// Parse the response
	var response WorkerResponse
	if err := json.Unmarshal([]byte(cleanedResult), &response); err != nil {
		errMsg := fmt.Sprintf("Worker %d failed to parse response: %v. Response was: %s", s.workerID, err, cleanedResult)
		log.Printf("[Worker %d] JSON parse error: %v. Full response: %s", s.workerID, err, cleanedResult)
		stream.Send(&pb.TaskResponse{
			TaskId: req.TaskId,
			Error:  errMsg,
			Status: pb.WorkerStatus_FAILED,
		})
		return err
	}

	// Send completion status with structured result
	resultBytes, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %v", err)
	}

	if err := stream.Send(&pb.TaskResponse{
		TaskId: req.TaskId,
		Result: string(resultBytes),
		Status: pb.WorkerStatus_COMPLETED,
	}); err != nil {
		return err
	}

	log.Printf("[Worker %d] Completed task: %s", s.workerID, req.TaskId)
	return nil
}

// getAPIKeyForProvider returns the appropriate API key for the worker's provider
// Falls back to the provided default key if provider-specific key is not found
func (s *WorkerServer) getAPIKeyForProvider(defaultKey string) string {
	var envVar string
	switch s.provider {
	case "openai":
		envVar = "OPENAI_API_KEY"
	case "anthropic":
		envVar = "ANTHROPIC_API_KEY"
	case "google":
		envVar = "GOOGLE_API_KEY"
		if os.Getenv(envVar) == "" {
			envVar = "GEMINI_API_KEY"
		}
	case "cohere":
		envVar = "COHERE_API_KEY"
	case "mistral":
		envVar = "MISTRAL_API_KEY"
	default:
		return defaultKey
	}

	if apiKey := os.Getenv(envVar); apiKey != "" {
		return apiKey
	}

	// Fallback to default key if provider-specific key not found
	return defaultKey
}
