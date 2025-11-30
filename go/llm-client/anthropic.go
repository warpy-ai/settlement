package llmclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient implements LLMClient for Anthropic Claude
type AnthropicClient struct {
	model string
}

// NewAnthropicClient creates a new Anthropic client
func NewAnthropicClient() *AnthropicClient {
	return &AnthropicClient{
		model: "claude-sonnet-4-5-20250929", // default - actual API model ID
	}
}

// SetModel sets the model to use
func (c *AnthropicClient) SetModel(model string) {
	c.model = model
}

// Call performs a non-streaming Anthropic API call
func (c *AnthropicClient) Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error) {
	client := anthropic.NewClient(
		option.WithAPIKey(apiKey),
	)

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 1024,
		System: []anthropic.TextBlockParam{
			{
				Text: systemPrompt,
			},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(userMessage),
			),
		},
	})

	if err != nil {
		return "", err
	}

	// Extract text content from response
	if len(message.Content) > 0 {
		contentBlock := message.Content[0]
		if contentBlock.Type == "text" {
			return contentBlock.Text, nil
		}
	}

	return "", fmt.Errorf("no text content in response")
}

// CallStreaming performs a streaming Anthropic API call
// Note: Streaming implementation using aisdk-go can be added later
func (c *AnthropicClient) CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error {
	// TODO: Implement streaming using aisdk-go
	return fmt.Errorf("streaming not yet implemented for Anthropic")
}
