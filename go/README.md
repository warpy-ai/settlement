# Multi-Agent Task Processing System

A distributed system that uses gRPC and multiple LLM providers to process tasks across multiple worker agents, coordinated by a supervisor. The system features advanced consensus mechanisms, intelligent worker management, and provider diversity for robust decision-making.

## Supported LLM Providers

| Provider | Models | Status |
|----------|--------|--------|
| **OpenAI** | gpt-4o, gpt-4o-mini | Reliable |
| **Anthropic** | claude-sonnet-4-5-20250929, claude-haiku-4-5, claude-opus-4-1 | Reliable |
| **Google** | gemini-2.5-flash, gemini-2.5-pro | Reliable |
| **Cohere** | command-a-03-2025, command-r | Reliable |
| **Mistral** | mistral-large-latest, mistral-small-latest | Reliable |

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
           │     Worker 1     │ │    Worker 2    │  │    Worker 3    │ │    Worker N    │
           │    (OpenAI)      │ │  (Anthropic)   │  │    (Google)    │ │   (Mistral)    │
           │  ┌────────────┐  │ │  ┌──────────┐  │  │  ┌──────────┐  │ │  ┌──────────┐  │
           │  │ gRPC Server│  │ │  │gRPC Serv │  │  │  │gRPC Serv │  │ │  │gRPC Serv │  │
           │  └────────────┘  │ │  └──────────┘  │  │  └──────────┘  │ │  └──────────┘  │
           │  ┌────────────┐  │ │  ┌──────────┐  │  │  ┌──────────┐  │ │  ┌──────────┐  │
           │  │ LLM Client │  │ │  │LLM Client│  │  │  │LLM Client│  │ │  │LLM Client│  │
           │  └────────────┘  │ │  └──────────┘  │  │  └──────────┘  │ │  └──────────┘  │
           └────────┬─────────┘ └───────┬────────┘  └──────┬─────────┘ └────────┬───────┘
                    │                   │                  │                    │
                    ▼                   ▼                  ▼                    ▼
              ┌──────────┐        ┌──────────┐       ┌──────────┐        ┌──────────┐
              │ OpenAI   │        │Anthropic │       │  Google  │        │ Mistral  │
              │   API    │        │   API    │       │   API    │        │   API    │
              └──────────┘        └──────────┘       └──────────┘        └──────────┘

Features:
- Supervisor manages worker pool and task queue
- Workers run independently with their own gRPC servers
- Each worker assigned a random provider/model for diversity
- Provider cap ensures no single LLM dominates (max 2 workers per provider)
- Dynamic scaling from 3 to 15 workers
- Consensus-based result aggregation across different AI models
```

## Key Features

### 1. Multi-Provider LLM Support

- **5 providers** supported out of the box
- Random provider/model assignment for diversity
- Provider diversity cap (max 2 workers per provider per task)
- Automatic fallback to reliable models
- Per-worker model configuration via environment variables

### 2. Distributed Task Processing

- Multiple worker nodes for parallel processing
- gRPC-based communication
- Automatic worker scaling (3-15 workers)
- Health monitoring and recovery
- Task streaming and progress tracking

### 3. Advanced Consensus Mechanism

- Semantic similarity matching with configurable thresholds
- Weighted voting based on worker confidence
- Adaptive consensus thresholds
- Multiple consensus strategies:
  - **Exact Match**: For precise matches
  - **Semantic Match**: For text with similar meaning
  - **Numeric Match**: For calculations with tolerance
  - **Merge Match**: For synthesizing responses from multiple models
- Cross-provider consensus for robust results

### 4. Worker Management

- Dynamic worker scaling
- Health monitoring with heartbeats
- Automatic recovery from failures
- Load balancing across providers
- Status tracking (available, busy, offline)
- Provider/model tracking per worker

## Prerequisites

1. Go 1.22 or later
2. Protocol Buffers compiler (protoc)
3. API keys for desired providers
4. gRPC tools

## Configuration

### Environment Variables

```env
# Required: At least one LLM provider API key
OPENAI_API_KEY=sk-proj-...
ANTHROPIC_API_KEY=sk-ant-api03-...
GOOGLE_API_KEY=AIzaSy...
COHERE_API_KEY=...
MISTRAL_API_KEY=...

# Optional: Control model selection
LLM_PROVIDER=random              # "random", "openai", "anthropic", "google", "cohere", "mistral"
USE_RELIABLE_MODELS_ONLY=true    # Only use tested/verified models
```

### Supervisor Configuration

```go
type SupervisorConfig struct {
    NumWorkers  int    // Initial worker count (default: 3)
    MaxWorkers  int    // Maximum workers (default: 15)
    APIKey      string // Primary API key (for task analysis)
    WorkDir     string // Working directory
    LLMProvider string // Provider selection: "random", "openai", "anthropic", etc.
}
```

### Consensus Configuration

```go
type ConsensusConfig struct {
    MinimumAgreement       float64           // Base agreement threshold (0.0-1.0)
    TimeoutDuration        time.Duration     // Task timeout
    VotingStrategy         string            // Voting method
    MatchStrategy          ConsensusStrategy // Comparison method
    NumericTolerance       float64           // For numeric comparisons
    ExtractMergedReasoning bool              // Synthesize responses
}
```

## LLM Client Architecture

The `llm-client` package provides a unified interface for all providers:

```go
// LLMClient interface - implemented by all providers
type LLMClient interface {
    Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error)
    CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error
    SetModel(model string)
}

// Factory functions
client, err := llmclient.NewLLMClient("anthropic")
client, err := llmclient.NewLLMClientWithModel("openai", "gpt-4o-mini")

// Random selection from reliable models
config := llmclient.GetRandomLLMConfig()
// config.Provider = "google"
// config.Model = "gemini-2.5-flash"
```

## Task Processing Flow

1. **Task Submission**
   - Task analyzed for complexity
   - Worker count determined (3-15, always odd)
   - Consensus requirements set

2. **Worker Assignment**
   - Workers selected with provider diversity
   - Max 2 workers per provider per task
   - Each worker uses its assigned LLM

3. **Processing**
   - Workers process tasks independently
   - Different AI models provide diverse perspectives
   - Results streamed back to supervisor

4. **Consensus Building**
   - Responses grouped by similarity
   - Cross-provider voting applied
   - AI synthesis when votes are split
   - Final answer determined by agreement

5. **Result Delivery**
   - Consensus result returned
   - Worker metadata (provider/model) included
   - Alternatives recorded

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/tasks` | POST | Submit a new task |
| `/api/v1/tasks/{taskId}` | GET | Get task status and results |
| `/api/v1/tasks/{taskId}/cancel` | POST | Cancel a task |
| `/api/v1/batch` | POST | Submit batch of tasks |
| `/api/v1/workers` | GET | List all workers with provider info |
| `/api/v1/stats` | GET | System statistics |

## Example Usage

```go
// Create supervisor with multi-provider support
supervisor := core.NewSupervisor(core.SupervisorConfig{
    NumWorkers:  3,
    MaxWorkers:  15,
    APIKey:      os.Getenv("OPENAI_API_KEY"),
    WorkDir:     workDir,
    LLMProvider: "random", // Use diverse providers
})

supervisor.Start(ctx)

// Workers will be assigned random providers:
// [Supervisor] Started worker 1 (provider=anthropic, model=claude-haiku-4-5)
// [Supervisor] Started worker 2 (provider=google, model=gemini-2.5-flash)
// [Supervisor] Started worker 3 (provider=mistral, model=mistral-large-latest)

// Submit tasks - processed by diverse AI models
supervisor.SubmitTask("What is the best programming language for web development?")

// Collect results - consensus from multiple AI perspectives
for result := range supervisor.GetResults() {
    if result.Error != nil {
        log.Printf("Error: %v", result.Error)
        continue
    }
    fmt.Printf("Consensus Result: %s\n", result.Result)
}

supervisor.Close()
```

## Adding New Providers

1. Create a new file in `llm-client/` (e.g., `newprovider.go`)
2. Implement the `LLMClient` interface
3. Add provider constant in `interfaces.go`
4. Register in factory function in `factory.go`
5. Add models to `models.go`

## Error Handling

- Worker failure recovery with automatic retry
- Provider-specific error handling
- Rate limit management per provider
- Timeout handling
- Task reprocessing with fresh workers

## Best Practices

1. **Provider Diversity**
   - Configure all available API keys
   - Use `LLM_PROVIDER=random` for best diversity
   - Enable `USE_RELIABLE_MODELS_ONLY=true` for stability

2. **Scaling**
   - Start with 3 workers minimum
   - Use odd numbers for clear consensus
   - Monitor provider rate limits

3. **Consensus**
   - Higher worker counts for important decisions
   - Adjust agreement thresholds per task type
   - Enable merge strategy for subjective questions

## License

MIT License
