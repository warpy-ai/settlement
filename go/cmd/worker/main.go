package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"settlement-core/core"
	pb "settlement-core/proto/gen/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	workerID := flag.Int("id", 1, "Worker ID")
	flag.Parse()

	port := 50051 + *workerID - 1
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("[Worker %d] Failed to listen: %v", *workerID, err)
	}

	server := grpc.NewServer()
	workerServer := core.NewWorkerServer(*workerID)
	pb.RegisterWorkerServiceServer(server, workerServer)

	// Register health service
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)

	log.Printf("[Worker %d] Starting worker server on port %d", *workerID, port)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("[Worker %d] Failed to serve: %v", *workerID, err)
	}
}
