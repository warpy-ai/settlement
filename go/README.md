# Multi-Agent Task Processing System

A distributed system that uses gRPC and OpenAI to process tasks across multiple worker agents, coordinated by a supervisor. The system features advanced consensus mechanisms and intelligent worker management.

## System Architecture

```
                                ┌─────────────────────────────┐
                                │         Supervisor          │
                                │ ┌──────────┐    ┌─────────┐ │
                                │ │   Pool   │    │  Queue  │ │
                                │ │ Manager  │    │ Manager │ │
                                │ └──────────┘    └─────────┘ │
                                │        ▲            ▲       │
                                └────────┼────────────┼───────┘
                                         │            │
                     ┌──────────────────────────────────────────────────────────┐
                     │                    │                │                    │
           ┌─────────┴────────┐ ┌─────────┴──────┐  ┌──────┴─────────┐ ┌────────┴───────┐
           │     Worker 1     │ │    Worker 2    │  |    Worker 3    │ │    Worker N    │
           │  ┌────────────┐  │ │  ┌──────────┐  │  |  ┌──────────┐  │ │  ┌──────────┐  │
           │  │ gRPC Serve │  │ │  |gRPC Serv │  │  |  |gRPC Serv │  │ │  |gRPC Serv │  │
           │  └────────────┘  │ │  └──────────┘  │  |  └──────────┘  │ │  └──────────┘  │
           │  ┌────────────┐  │ │  ┌──────────┐  │  |  ┌──────────┐  │ │  ┌──────────┐  │
           │  │Task Hand   │  | |  │Task Hand │  │  |  | Task Hand│  │ │  |Task Hand │  │
           │  └────────────┘  │ │  └──────────┘  │  |  └──────────┘  │ │  └──────────┘  │
           └────────┬─────────┘ └───────┬────────┘  └──────┬─────────┘ └────────┬───────┘
                    │                   │                  │                     │
                    └───────────────────┼──────────────────┼─────────────────────┘
                                        │                  │
                             ┌──────────┴──────────────────┴──────┐
                             │           OpenAI API               │
                             │  ┌─────────────────────────────┐   │
                             │  │        GPT-4 Model          │   │
                             │  └─────────────────────────────┘   │
                             └────────────────────────────────────┘

Features:
- Supervisor manages worker pool and task queue
- Workers run independently with their own gRPC servers
- Dynamic scaling from 3 to N workers
- Task streaming and health monitoring
- Consensus-based result aggregation
- Automatic worker recovery and load balancing
```

## Key Features

### 1. Distributed Task Processing

- Multiple worker nodes for parallel processing
- gRPC-based communication
- Automatic worker scaling (3-15 workers)
- Health monitoring and recovery
- Task streaming and progress tracking

### 2. Advanced Consensus Mechanism

- Semantic similarity matching with configurable thresholds
- Weighted voting based on worker confidence
- Adaptive consensus thresholds
- Multiple consensus strategies:
  - Exact Match: For precise matches
  - Semantic Match: For text with similar meaning
  - Numeric Match: For calculations with tolerance
- Group merging for similar responses
- Confidence-weighted voting
- Automatic threshold adjustment based on:
  - Number of groups
  - Response similarity
  - Voting patterns

### 3. Worker Management

- Dynamic worker scaling
- Health monitoring with heartbeats
- Automatic recovery from failures
- Load balancing
- Status tracking (available, busy, offline)
- Voting power assignment
- Connection management

### 4. Task Analysis

- Automatic complexity assessment
- Worker count determination
- Timeout calculation
- Match strategy selection
- Agreement threshold adjustment

## Prerequisites

1. Go 1.22 or later
2. Protocol Buffers compiler (protoc)
3. OpenAI API key
4. gRPC tools

## Configuration

### Environment Variables

```env
OPENAI_API_KEY=your-api-key-here
```

### Supervisor Configuration

```go
type SupervisorConfig struct {
    NumWorkers int    // Initial worker count (default: 3)
    MaxWorkers int    // Maximum workers (default: 15)
    APIKey     string // OpenAI API key
    WorkDir    string // Working directory
}
```

### Consensus Configuration

```go
type ConsensusConfig struct {
    MinimumAgreement float64          // Base agreement threshold
    TimeoutDuration  time.Duration    // Task timeout
    VotingStrategy   string           // Voting method
    MatchStrategy    ConsensusStrategy // Comparison method
    NumericTolerance float64          // For numeric comparisons
}
```

## Task Processing Flow

1. **Task Submission**

   - Task analyzed for complexity
   - Worker count determined
   - Consensus requirements set

2. **Worker Assignment**

   - Available workers selected
   - Task distributed to workers
   - Worker status tracked

3. **Processing**

   - Workers process tasks independently
   - Results streamed back to supervisor
   - Status updates maintained

4. **Consensus Building**

   - Responses grouped by similarity
   - Weighted voting applied
   - Groups merged when appropriate
   - Consensus determined by:
     - Agreement percentage
     - Vote difference
     - Confidence scores

5. **Result Delivery**
   - Final result selected
   - Alternatives recorded
   - Metadata collected

## Error Handling

- Worker failure recovery
- Rate limit management
- Timeout handling
- Connection retry logic
- Task reprocessing

## Monitoring

- Worker health tracking
- Task progress monitoring
- Resource usage tracking
- Status logging
- Performance metrics

## Best Practices

1. **Configuration**

   - Use environment variables
   - Secure API keys
   - Adjust thresholds based on needs

2. **Scaling**

   - Start with minimum workers
   - Scale based on load
   - Monitor resource usage
   - Handle rate limits

3. **Consensus**

   - Adjust thresholds for task type
   - Consider response similarity
   - Balance accuracy vs. speed
   - Monitor group formation

4. **Error Handling**
   - Implement retries
   - Clean up resources
   - Log failures
   - Handle timeouts

## Example Usage

```go
supervisor := core.NewSupervisor(core.SupervisorConfig{
    NumWorkers: 3,
    MaxWorkers: 15,
    APIKey:     os.Getenv("OPENAI_API_KEY"),
    WorkDir:    workDir,
})

supervisor.Start(ctx)

// Submit tasks
supervisor.SubmitTask("Analyze the impact of AI on employment...")
supervisor.SubmitTask("Calculate the compound interest...")
supervisor.SubmitTask("Translate this technical document...")

// Collect results
for result := range supervisor.GetResults() {
    if result.Error != nil {
        log.Printf("Error: %v", result.Error)
        continue
    }
    fmt.Printf("Result: %s\n", result.Result)
}

supervisor.Close()
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Submit a pull request

## License

MIT License
