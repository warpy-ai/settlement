package core

import (
	"context"
	"os"

	llmclient "settlement-core/llm-client"
)

// CallOpenAIFunction sends a task to the OpenAI API
// This function is kept for backward compatibility and uses the default LLM provider
func CallOpenAIFunction(ctx context.Context, task, apiKey string) (string, error) {
	// Get provider from environment or default to OpenAI
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "openai"
	}

	// Create LLM client
	llmClient, err := llmclient.NewLLMClient(provider)
	if err != nil {
		return "", err
	}

	// Call with default system prompt
	systemPrompt := "You are an AI language model that processes tasks and returns clear, concise responses."
	return llmClient.Call(ctx, systemPrompt, task, apiKey)
}
