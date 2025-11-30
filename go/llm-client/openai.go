package llmclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAIClient implements LLMClient for OpenAI
type OpenAIClient struct {
	model string
}

// NewOpenAIClient creates a new OpenAI client
func NewOpenAIClient() *OpenAIClient {
	return &OpenAIClient{
		model: "gpt-4o", // default
	}
}

// SetModel sets the model to use
func (c *OpenAIClient) SetModel(model string) {
	c.model = model
}

// Call performs a non-streaming OpenAI API call
func (c *OpenAIClient) Call(ctx context.Context, systemPrompt, userMessage, apiKey string) (string, error) {
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
	)

	messages := []openai.ChatCompletionMessageParamUnion{
		{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Content: openai.ChatCompletionSystemMessageParamContentUnion{
					OfString: openai.String(systemPrompt),
				},
			},
		},
		{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Content: openai.ChatCompletionUserMessageParamContentUnion{
					OfString: openai.String(userMessage),
				},
			},
		},
	}

	resp, err := client.Chat.Completions.New(
		ctx,
		openai.ChatCompletionNewParams{
			Model:    c.model,
			Messages: messages,
		},
	)

	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}

// CallStreaming performs a streaming OpenAI API call
// Note: Streaming implementation using aisdk-go can be added later
func (c *OpenAIClient) CallStreaming(ctx context.Context, systemPrompt, userMessage, apiKey string, w http.ResponseWriter) error {
	// TODO: Implement streaming using aisdk-go
	return fmt.Errorf("streaming not yet implemented for OpenAI")
}
