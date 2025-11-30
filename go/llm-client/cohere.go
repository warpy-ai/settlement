package llmclient

import (
	"context"
	"fmt"
	"io"
	"net/http"

	coheregov2 "github.com/cohere-ai/cohere-go/v2"
	"github.com/cohere-ai/cohere-go/v2/client"
	cohereoption "github.com/cohere-ai/cohere-go/v2/option"
)

// CohereClient implements LLMClient for Cohere
type CohereClient struct {
	model string
}

// NewCohereClient creates a new Cohere client
func NewCohereClient() *CohereClient {
	return &CohereClient{
		model: "command-a-03-2025", // default - latest model
	}
}

// SetModel sets the model to use
func (c *CohereClient) SetModel(model string) {
	c.model = model
}

// Call performs a non-streaming Cohere API call
func (c *CohereClient) Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error) {
	cohereClient := client.NewClient(
		cohereoption.WithToken(apiKey),
	)

	// Combine system prompt and user message
	fullPrompt := systemPrompt + "\n\n" + userMessage

	// Use non-streaming chat endpoint
	resp, err := cohereClient.Chat(ctx, &coheregov2.ChatRequest{
		Message: fullPrompt,
		Model:   coheregov2.String(c.model),
	})
	if err != nil {
		return "", err
	}

	if resp.Text != "" {
		return resp.Text, nil
	}

	return "", fmt.Errorf("no response from Cohere")
}

// CallStreaming performs a streaming Cohere API call
func (c *CohereClient) CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error {
	cohereClient := client.NewClient(
		cohereoption.WithToken(apiKey),
	)

	// Combine system prompt and user message
	fullPrompt := systemPrompt + "\n\n" + userMessage

	stream, err := cohereClient.ChatStream(ctx, &coheregov2.ChatStreamRequest{
		Message: fullPrompt,
		Model:   coheregov2.String(c.model),
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Stream responses
	for {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if event.TextGeneration != nil && event.TextGeneration.Text != "" {
			// Format as Server-Sent Events
			fmt.Fprintf(w, "data: %s\n\n", event.TextGeneration.Text)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if event.StreamEnd != nil {
			break
		}
	}

	return nil
}
