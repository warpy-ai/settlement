package llmclient

import (
	"fmt"
)

// NewLLMClient creates a new LLM client based on the provider string
func NewLLMClient(provider string) (LLMClient, error) {
	switch Provider(provider) {
	case ProviderOpenAI, "":
		// Default to OpenAI for backward compatibility
		return NewOpenAIClient(), nil
	case ProviderAnthropic:
		return NewAnthropicClient(), nil
	case ProviderGoogle:
		return NewGoogleClient(), nil
	case ProviderCohere:
		return NewCohereClient(), nil
	case ProviderMistral:
		return NewMistralClient(), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s. Supported providers: openai, anthropic, google, cohere, mistral", provider)
	}
}

// NewLLMClientWithModel creates a new LLM client with a specific model
func NewLLMClientWithModel(provider, model string) (LLMClient, error) {
	client, err := NewLLMClient(provider)
	if err != nil {
		return nil, err
	}
	client.SetModel(model)
	return client, nil
}

// NewLLMClientFromProvider creates a new LLM client from a Provider type
func NewLLMClientFromProvider(provider Provider) (LLMClient, error) {
	return NewLLMClient(string(provider))
}

