package core

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// CallOpenAIFunction sends a task to the OpenAI API
func CallOpenAIFunction(ctx context.Context, task, apiKey string) (string, error) {
	client := openai.NewClient(apiKey)

	resp, err := client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: openai.GPT4,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: "You are an AI language model that processes tasks and returns clear, concise responses.",
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: task,
				},
			},
		},
	)

	if err != nil {
		return "", err
	}

	return resp.Choices[0].Message.Content, nil
}
