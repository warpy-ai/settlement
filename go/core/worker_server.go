package core

import (
	"fmt"
	"log"
	pb "settlement-core/proto/gen/proto"
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

	// Process task using OpenAI
	result, err := CallOpenAIFunction(stream.Context(), req.Content, req.ApiKey)
	if err != nil {
		errMsg := fmt.Sprintf("Worker %d failed: %v", s.workerID, err)
		stream.Send(&pb.TaskResponse{
			TaskId: req.TaskId,
			Error:  errMsg,
			Status: pb.WorkerStatus_FAILED,
		})
		return err
	}

	// Send completion status with result
	if err := stream.Send(&pb.TaskResponse{
		TaskId: req.TaskId,
		Result: result,
		Status: pb.WorkerStatus_COMPLETED,
	}); err != nil {
		return err
	}

	log.Printf("[Worker %d] Completed task: %s", s.workerID, req.TaskId)
	return nil
}
