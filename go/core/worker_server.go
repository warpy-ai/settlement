package core

import (
	"encoding/json"
	"fmt"
	"log"
	pb "settlement-core/proto/gen/proto"
	"strings"
)

// WorkerServer implements the gRPC worker service
type WorkerServer struct {
	pb.UnimplementedWorkerServiceServer
	workerID int
}

// NewWorkerServer creates a new worker server instance
func NewWorkerServer(id int) *WorkerServer {
	return &WorkerServer{workerID: id}
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

For translations:
- decision: the translated text
- metadata: source_language, target_language, formality_level
- alternatives: alternative translations

For analysis:
- decision: clear, concise conclusion
- metadata: key_factors, data_sources, confidence_factors
- alternatives: alternative viewpoints

For calculations:
- decision: the final calculated value
- metadata: formula_used, units, precision
- alternatives: results with different methods

Respond ONLY with the JSON object, no other text.`, category, category)

	// Process task using OpenAI
	result, err := CallOpenAIFunction(stream.Context(), fmt.Sprintf("%s\n\nTask: %s", systemPrompt, req.Content), req.ApiKey)
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

	// Parse the response
	var response WorkerResponse
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		errMsg := fmt.Sprintf("Worker %d failed to parse response: %v", s.workerID, err)
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
