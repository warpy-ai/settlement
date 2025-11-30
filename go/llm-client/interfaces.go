package llmclient

import (
	"context"
	"net/http"
)

// LLMClient defines the interface for all LLM providers
type LLMClient interface {
	// Call performs a non-streaming LLM call
	Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error)
	
	// CallStreaming performs a streaming LLM call (optional, for future use)
	CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error
	
	// SetModel sets the model to use (optional, some clients may not support this)
	SetModel(model string)
}

// Provider represents the LLM provider type
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGoogle    Provider = "google"
	ProviderCohere    Provider = "cohere"
	ProviderMistral   Provider = "mistral"
)

