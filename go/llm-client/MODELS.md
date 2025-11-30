# LLM Models Configuration

This document describes the available models and their status.

## Model Status

### ✅ Reliable Models (Tested and Working)

These models are known to work reliably:

- **OpenAI**: `gpt-4o`, `gpt-4o-mini`
- **Anthropic**: `claude-3-5-sonnet-20241022`, `claude-3-5-haiku-20241022`

### ⚠️ Experimental Models (May Have Issues)

These models are available but may have compatibility issues:

- **Google**: Model names may vary by API version. Current attempts:
  - `gemini-2.0-flash-exp` (experimental)
  - `gemini-1.5-pro`
  - `gemini-1.5-flash` (may not work with v1beta API)

- **Cohere**: `command-r-plus`, `command-r`, `command`
- **Mistral**: `mistral-large-latest`, `mistral-medium-latest`, `mistral-small-latest`

## Configuration

### Use Only Reliable Models

To restrict workers to only use tested, reliable models, set the environment variable:

```bash
export USE_RELIABLE_MODELS_ONLY=true
```

This will prevent workers from using Google, Cohere, or Mistral models until they are verified to work correctly.

### Model Selection

By default, workers randomly select from all available models. If a model fails:
1. The error is logged
2. For Google models, a fallback model is attempted
3. If all attempts fail, the task fails with an error

## Troubleshooting

### Google Model Errors

If you see errors like:
```
models/gemini-1.5-flash is not found for API version v1beta
```

This indicates the model name doesn't match what the API expects. Options:
1. Set `USE_RELIABLE_MODELS_ONLY=true` to exclude Google models
2. Check Google's API documentation for correct model names
3. Verify your API key has access to the requested model

### JSON Parsing Errors

If you see JSON parsing errors:
- The model may be returning responses in an unexpected format
- Check the logs for the full response to see what was returned
- Some models may need different prompting to ensure JSON output

## Adding New Models

To add new models, update `providerModels` in `models.go`:

```go
ProviderOpenAI: {
    "gpt-4o",
    "new-model-name", // Add here
},
```

Then test the model to ensure it works before adding to `GetReliableModels()`.

