# Multi-Agent Task Processing System

A distributed system that uses gRPC and OpenAI to process tasks across multiple worker agents, coordinated by a supervisor.

## System Architecture

```
                                   ┌─────────────┐
                                   │             │
                                   │ Supervisor  │
                                   │             │
                                   └──────┬──────┘
                                         │
                         ┌───────────────┼───────────────┐
                         │               │               │
                   ┌─────┴────┐    ┌─────┴────┐    ┌─────┴────┐
                   │ Worker 1  │    │ Worker 2  │    │ Worker 3  │
                   └─────┬────┘    └─────┬────┘    └─────┬────┘
                         │               │               │
                         └───────────────┼───────────────┘
                                        │
                                  ┌─────┴─────┐
                                  │  OpenAI   │
                                  │   API     │
                                  └───────────┘
```

## Features

- Distributed task processing with multiple workers
- gRPC-based communication
- Health monitoring and automatic worker recovery
- Graceful shutdown handling
- Task progress tracking
- OpenAI API integration
- Concurrent task processing

## Prerequisites

1. Go 1.22 or later
2. Protocol Buffers compiler (protoc)
3. OpenAI API key
4. gRPC tools

## Step-by-Step Setup

### 1. Project Structure

Create the following directory structure:

```
go/
├── cmd/
│   ├── main.go          # Supervisor entry point
│   └── worker/
│       └── main.go      # Worker entry point
├── core/
│   ├── supervisor.go    # Supervisor implementation
│   ├── worker_server.go # Worker implementation
│   ├── openai_client.go # OpenAI integration
│   └── types.go         # Common types
├── proto/
│   └── worker.proto     # Protocol buffer definitions
├── .env                 # Environment configuration
├── .env.example         # Environment template
├── go.mod              # Go module file
└── README.md           # Documentation
```

### 2. Install Dependencies

```bash
# Install Protocol Buffers compiler
brew install protobuf

# Install Go gRPC tools
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

### 3. Protocol Buffer Definition

Create `proto/worker.proto`:

```protobuf
syntax = "proto3";

package worker;
option go_package = "settlement-core/proto";

service WorkerService {
  rpc ProcessTask (TaskRequest) returns (stream TaskResponse) {}
}

message TaskRequest {
  string task_id = 1;
  string content = 2;
  string api_key = 3;
}

message TaskResponse {
  string task_id = 1;
  string result = 2;
  string error = 3;
  WorkerStatus status = 4;
}

enum WorkerStatus {
  UNKNOWN = 0;
  PROCESSING = 1;
  COMPLETED = 2;
  FAILED = 3;
}
```

### 4. Generate gRPC Code

```bash
mkdir -p proto/gen
protoc --go_out=proto/gen --go_opt=paths=source_relative \
       --go-grpc_out=proto/gen --go-grpc_opt=paths=source_relative \
       proto/worker.proto
```

### 5. Environment Setup

Create `.env` file:

```env
OPENAI_API_KEY=your-api-key-here
```

### 6. Implementation Steps

1. **Common Types (`core/types.go`)**

   - Define shared types used across the system
   - Implement task result structures

2. **OpenAI Integration (`core/openai_client.go`)**

   - Implement OpenAI API client
   - Handle API requests and responses

3. **Worker Server (`core/worker_server.go`)**

   - Implement gRPC service
   - Handle task processing
   - Manage OpenAI API calls

4. **Supervisor (`core/supervisor.go`)**

   - Manage worker lifecycle
   - Handle task distribution
   - Monitor worker health
   - Implement graceful shutdown

5. **Worker Entry Point (`cmd/worker/main.go`)**

   - Set up gRPC server
   - Initialize worker service
   - Handle health checks

6. **Main Entry Point (`cmd/main.go`)**
   - Initialize supervisor
   - Submit tasks
   - Handle results

## Running the System

1. **Build the Project**

```bash
go mod tidy
```

2. **Start the System**

```bash
go run cmd/main.go
```

The supervisor will:

- Build the worker binary
- Start worker processes
- Establish connections
- Begin processing tasks

## Task Processing Flow

1. Supervisor receives tasks via `SubmitTask`
2. Tasks are distributed to available workers
3. Workers process tasks using OpenAI API
4. Results are streamed back to supervisor
5. Supervisor collects and aggregates results

## Monitoring and Management

The system provides:

- Real-time task progress tracking
- Worker health monitoring
- Automatic worker recovery
- Graceful shutdown handling

## Error Handling

The system handles:

- Worker failures
- Connection issues
- API errors
- Task timeouts

## Best Practices

1. **Configuration**

   - Use environment variables for sensitive data
   - Keep API keys secure

2. **Monitoring**

   - Watch worker health
   - Track task completion rates
   - Monitor resource usage

3. **Scaling**
   - Adjust worker count based on load
   - Monitor API rate limits
   - Balance task distribution

## Contributing

1. Fork the repository
2. Create a feature branch
3. Submit a pull request

## License

MIT License
