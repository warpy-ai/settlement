package llmclient

import (
	"math/rand"
	"time"
)

// ModelConfig represents a model configuration for a provider
type ModelConfig struct {
	Provider string
	Model    string
}

// Available models for each provider
// Only include models that are known to work reliably
var providerModels = map[Provider][]string{
	ProviderOpenAI: {
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4-turbo",
		"gpt-3.5-turbo",
	},
	ProviderAnthropic: {
		"claude-sonnet-4-5-20250929",  // Latest Sonnet 4.5 (actual API model ID)
		"claude-haiku-4-5",             // Haiku 4.5
		"claude-opus-4-1",              // Opus 4.1
		"claude-3-5-sonnet-20240620",  // Legacy fallback
		"claude-3-opus-20240229",       // Legacy fallback
		"claude-3-sonnet-20240229",     // Legacy fallback
	},
	// Google models - using model names that work with genai SDK
	// Note: Some models may require specific API versions or may not be available in all regions
	ProviderGoogle: {
		"gemini-2.5-pro",           // Latest Pro model
		"gemini-3-pro-preview",     // Preview model
		"gemini-2.5-flash",         // Latest Flash model
		"gemini-2.0-flash-exp",      // Experimental fallback
		"gemini-1.5-pro",           // Legacy fallback
		"gemini-1.5-flash",         // Legacy fallback
	},
	ProviderCohere: {
		"command-a-03-2025",  // Latest model
		"command-r-plus",     // Legacy fallback
		"command-r",          // Legacy fallback
		"command",            // Legacy fallback
	},
	ProviderMistral: {
		"large-2",                // Latest large model (actual API ID)
		"mistral-medium-latest",  // Medium model
		"mistral-large-latest",   // Legacy fallback
		"mistral-small-latest",   // Small model
	},
}

// GetRandomLLMConfig returns a random provider and model
// For now, we'll bias towards more reliable providers to avoid errors
func GetRandomLLMConfig() ModelConfig {
	// Initialize random seed
	rand.Seed(time.Now().UnixNano())

	// Get all available providers
	providers := make([]Provider, 0, len(providerModels))
	for provider := range providerModels {
		providers = append(providers, provider)
	}

	// Select random provider
	selectedProvider := providers[rand.Intn(len(providers))]

	// Get models for selected provider
	models := providerModels[selectedProvider]

	// Select random model
	selectedModel := models[rand.Intn(len(models))]

	return ModelConfig{
		Provider: string(selectedProvider),
		Model:    selectedModel,
	}
}

// GetModelsForProvider returns all available models for a given provider
func GetModelsForProvider(provider Provider) []string {
	if models, ok := providerModels[provider]; ok {
		return models
	}
	return []string{}
}

// GetReliableModels returns only models that are known to work reliably
// This can be used to restrict random selection to tested models
func GetReliableModels() map[Provider][]string {
	return map[Provider][]string{
		ProviderOpenAI: {
			"gpt-4o",
			"gpt-4o-mini",
		},
		ProviderAnthropic: {
			"claude-sonnet-4-5-20250929",  // Latest Sonnet 4.5 (actual API model ID)
			"claude-haiku-4-5",           // Haiku 4.5
			"claude-opus-4-1",            // Opus 4.1
		},
		// Temporarily exclude Google until we verify model names work correctly
		// ProviderGoogle: {
		// 	"gemini-2.0-flash-exp",
		// },
	}
}

// GetRandomLLMConfigFromMap returns a random provider and model from a specific model map
func GetRandomLLMConfigFromMap(modelMap map[Provider][]string) ModelConfig {
	// Initialize random seed
	rand.Seed(time.Now().UnixNano())

	// Get all available providers from the map
	providers := make([]Provider, 0, len(modelMap))
	for provider := range modelMap {
		providers = append(providers, provider)
	}

	if len(providers) == 0 {
		// Fallback to default if map is empty
		return GetRandomLLMConfig()
	}

	// Select random provider
	selectedProvider := providers[rand.Intn(len(providers))]

	// Get models for selected provider
	models := modelMap[selectedProvider]

	if len(models) == 0 {
		// Fallback to default if no models for this provider
		return GetRandomLLMConfig()
	}

	// Select random model
	selectedModel := models[rand.Intn(len(models))]

	return ModelConfig{
		Provider: string(selectedProvider),
		Model:    selectedModel,
	}
}
