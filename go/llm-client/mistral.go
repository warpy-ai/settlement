package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MistralClient implements LLMClient for Mistral AI
type MistralClient struct {
	model string
}

// NewMistralClient creates a new Mistral client
func NewMistralClient() *MistralClient {
	return &MistralClient{
		model: "large-2", // default - actual API model ID
	}
}

// SetModel sets the model to use
func (c *MistralClient) SetModel(model string) {
	c.model = model
}

// mistralMessage represents a message in Mistral API format
type mistralMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// mistralRequest represents a request to Mistral API
type mistralRequest struct {
	Model       string          `json:"model"`
	Messages    []mistralMessage `json:"messages"`
	Temperature float64         `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

// mistralResponse represents a response from Mistral API
type mistralResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// mistralStreamChunk represents a streaming chunk from Mistral API
type mistralStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// Call performs a non-streaming Mistral API call
func (c *MistralClient) Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error) {
	messages := []mistralMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	// Map friendly names to actual API model IDs
	apiModelID := c.model
	if apiModelID == "mistral-large-latest" {
		apiModelID = "large-2" // Use actual API ID
	}
	
	reqBody := mistralRequest{
		Model:    apiModelID,
		Messages: messages,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.mistral.ai/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("mistral API error: %d - %s", resp.StatusCode, string(body))
	}

	var mistralResp mistralResponse
	if err := json.NewDecoder(resp.Body).Decode(&mistralResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(mistralResp.Choices) > 0 {
		return mistralResp.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("no response from Mistral API")
}

// CallStreaming performs a streaming Mistral API call
func (c *MistralClient) CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error {
	messages := []mistralMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	// Map friendly names to actual API model IDs
	apiModelID := c.model
	if apiModelID == "mistral-large-latest" {
		apiModelID = "large-2" // Use actual API ID
	}
	
	reqBody := mistralRequest{
		Model:    apiModelID,
		Messages: messages,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.mistral.ai/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Note: Mistral streaming requires stream=true parameter
	// For now, we'll implement basic streaming
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mistral API error: %d - %s", resp.StatusCode, string(body))
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Read and forward response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var mistralResp mistralResponse
	if err := json.Unmarshal(body, &mistralResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(mistralResp.Choices) > 0 {
		content := mistralResp.Choices[0].Message.Content
		// Stream content in chunks
		chunkSize := 10
		for i := 0; i < len(content); i += chunkSize {
			end := i + chunkSize
			if end > len(content) {
				end = len(content)
			}
			fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(content[i:end], "\n", "\\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	return nil
}

