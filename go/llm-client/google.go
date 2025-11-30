package llmclient

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/genai"
)

// GoogleClient implements LLMClient for Google Gemini
type GoogleClient struct {
	model string
}

// NewGoogleClient creates a new Google client
func NewGoogleClient() *GoogleClient {
	return &GoogleClient{
		model: "gemini-2.5-flash", // default - latest Flash model
	}
}

// SetModel sets the model to use
func (c *GoogleClient) SetModel(model string) {
	c.model = model
}

// Call performs a non-streaming Google Gemini API call
func (c *GoogleClient) Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error) {
	config := &genai.ClientConfig{
		Backend: genai.BackendGeminiAPI,
		APIKey:  apiKey,
	}

	client, err := genai.NewClient(ctx, config)
	if err != nil {
		return "", err
	}

	// Combine system prompt and user message
	fullPrompt := systemPrompt + "\n\n" + userMessage

	// Create a chat session
	chat, err := client.Chats.Create(ctx, c.model, nil, nil)
	if err != nil {
		return "", err
	}

	// Send message and get response
	resp, err := chat.SendMessage(ctx, genai.Part{Text: fullPrompt})
	if err != nil {
		return "", err
	}

	// Extract text from response
	if resp != nil && resp.Text() != "" {
		return resp.Text(), nil
	}

	return "", fmt.Errorf("no content in response")
}

// CallStreaming performs a streaming Google Gemini API call
// Note: Streaming implementation using aisdk-go can be added later
func (c *GoogleClient) CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error {
	// TODO: Implement streaming using aisdk-go
	return fmt.Errorf("streaming not yet implemented for Google")
}
