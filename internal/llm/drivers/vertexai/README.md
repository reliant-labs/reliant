# Vertex AI Multi-Provider Driver

A comprehensive driver for Google Cloud Vertex AI that supports multiple model providers through Vertex AI Model Garden.

## Supported Models

### Gemini Models (via Vertex AI)
- **Gemini 2.5 Pro** (`vertex-gemini-2.5-pro`) - Most capable Gemini model
- **Gemini 2.5 Flash** (`vertex-gemini-2.5-flash`) - Fast, cost-effective
- **Gemini 2.0 Flash** (`vertex-gemini-2.0-flash`) - Latest stable flash model
- **Gemini 1.5 Pro** (`vertex-gemini-1.5-pro`) - Previous generation pro
- **Gemini 1.5 Flash** (`vertex-gemini-1.5-flash`) - Previous generation flash

### Claude Models (via Vertex AI Model Garden) — Planned

> **Not yet implemented.** The model IDs below are reserved for future use.
> Use the Anthropic direct driver or OpenRouter for Claude models today.

- **Claude 4.5 Sonnet** (`vertex-claude-4.5-sonnet`)
- **Claude 4 Sonnet** (`vertex-claude-4-sonnet`)
- **Claude 4.1 Opus** (`vertex-claude-4.1-opus`)
- **Claude 4 Opus** (`vertex-claude-4-opus`)
- **Claude 4.5 Haiku** (`vertex-claude-4.5-haiku`)
- **Claude 3.7 Sonnet** (`vertex-claude-3.7-sonnet`)
- **Claude 3.5 Haiku** (`vertex-claude-3.5-haiku`)

## Setup

### Prerequisites

1. **Google Cloud Project**: You need a GCP project with Vertex AI enabled
2. **Authentication**: Set up Google Cloud authentication using one of:
   - Application Default Credentials (recommended)
   - Service Account Key

### Environment Variables

```bash
# Required
export VERTEXAI_PROJECT="your-gcp-project-id"

# Optional (defaults to us-central1)
export VERTEXAI_LOCATION="us-central1"
```

### Google Cloud Authentication

#### Option 1: Application Default Credentials (Recommended)

```bash
# Install gcloud CLI
# https://cloud.google.com/sdk/docs/install

# Login and set application default credentials
gcloud auth application-default login
```

#### Option 2: Service Account

```bash
# Create a service account with Vertex AI permissions
gcloud iam service-accounts create vertexai-driver \
  --display-name="Vertex AI Driver"

# Grant Vertex AI User role
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="serviceAccount:vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"

# Create and download key
gcloud iam service-accounts keys create key.json \
  --iam-account=vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com

# Set the credentials
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
```

## Usage

### Basic Usage

The Vertex AI driver is automatically selected when you use a Vertex AI model ID:

```go
import (
    "github.com/reliant-labs/reliant/internal/llm/models"
)

// Use a Gemini model via Vertex AI
preferences := models.Preferences{
    {ModelID: models.VertexGemini25Flash},
}

// Claude via Vertex AI (planned, not yet implemented)
// preferences := models.Preferences{
//     {ModelID: models.VertexClaude45Sonnet},
// }
```

### Configuration Options

```go
// With custom temperature
preferences := models.Preferences{
    {
        ModelID: models.VertexGemini25Pro,
        Temperature: models.TempMedium(),
        TokenBudget: genai.Ptr(int64(8000)),
    },
}

// With safety settings
// Set VERTEXAI_SAFETY_LEVEL environment variable to:
// - "low": Block only high-risk content
// - "medium": Block medium and high-risk content (default)
// - "high": Block low, medium, and high-risk content
```

## Features

### Gemini Provider

✅ **Text Generation**: Full support for text-based interactions  
✅ **Streaming**: Real-time response streaming  
✅ **Function Calling**: Tool use with proper function declarations  
✅ **Multimodal**: Support for images via InlineData  
✅ **Safety Settings**: Configurable content filtering  
✅ **Token Counting**: Accurate usage metrics  

### Claude Provider

⏳ **Coming Soon**: Claude support via Vertex AI Anthropic API is planned for a future release

## Architecture

```
VertexAIClient
├── Gemini Provider (via google.golang.org/genai)
│   ├── SendMessages
│   ├── StreamResponse
│   └── Function Calling
└── Claude Provider (planned)
    └── Via Vertex AI Anthropic REST API
```

## Pricing

Vertex AI pricing varies by model. Refer to [Google Cloud Pricing](https://cloud.google.com/vertex-ai/pricing) for current rates.

**Note**: The costs in the model definitions are approximate and may not reflect current Vertex AI pricing.

## Limitations

1. **Claude Support**: Not yet implemented. Use the Anthropic direct driver or OpenRouter for Claude models.
2. **Caching**: Prompt caching is not yet implemented for Gemini models via Vertex AI.
3. **Reasoning**: Extended thinking features are not yet supported.

## Troubleshooting

### "VERTEXAI_PROJECT environment variable is required"

Set the `VERTEXAI_PROJECT` environment variable:
```bash
export VERTEXAI_PROJECT="your-project-id"
```

### "Failed to initialize Gemini client"

Check your Google Cloud authentication:
```bash
gcloud auth application-default login
```

### "Permission denied" errors

Ensure your account/service account has the `roles/aiplatform.user` role:
```bash
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="user:your-email@example.com" \
  --role="roles/aiplatform.user"
```

### Model not found

Ensure you're using the correct model ID with the `vertex-` prefix and that the model is available in your region.

## Development

### Running Tests

```bash
# Ensure environment variables are set
export VERTEXAI_PROJECT="your-test-project"
export VERTEXAI_LOCATION="us-central1"

# Run tests
go test ./internal/llm/drivers/vertexai/...
```

### Adding New Models

To add new models supported by Vertex AI:

1. Add model constants to `internal/llm/models/vertexai.go`
2. Update the `SupportedModels` array in `internal/llm/drivers/vertexai/models.go`
3. Register the model in the init function

## Contributing

When contributing to the Vertex AI driver:

1. Follow existing code patterns
2. Add tests for new functionality
3. Update documentation

## References

- [Vertex AI Documentation](https://cloud.google.com/vertex-ai/docs)
- [Google GenAI Go SDK](https://pkg.go.dev/google.golang.org/genai)
- [Vertex AI Model Garden](https://cloud.google.com/vertex-ai/docs/model-garden)
- [Anthropic on Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude)