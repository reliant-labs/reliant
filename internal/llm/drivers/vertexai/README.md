# Vertex AI Driver

Driver for Google Cloud Vertex AI supporting Gemini models. Claude models via Vertex AI Model Garden are **not yet implemented** — use the Anthropic direct driver or OpenRouter for Claude today.

## Supported Models

### Gemini Models
- **Gemini 2.5 Pro** (`vertex-gemini-2.5-pro`)
- **Gemini 2.5 Flash** (`vertex-gemini-2.5-flash`)
- **Gemini 2.0 Flash** (`vertex-gemini-2.0-flash`)
- **Gemini 1.5 Pro** (`vertex-gemini-1.5-pro`)
- **Gemini 1.5 Flash** (`vertex-gemini-1.5-flash`)

### Gemini Feature Support
- Text generation, streaming, function calling, multimodal (images via InlineData), safety settings, token counting

## Setup

### Environment Variables

```bash
# Required
export VERTEXAI_PROJECT="your-gcp-project-id"

# Optional (defaults to us-central1)
export VERTEXAI_LOCATION="us-central1"
```

### Authentication

**Option 1: Application Default Credentials (Recommended)**

```bash
gcloud auth application-default login
```

**Option 2: Service Account**

```bash
gcloud iam service-accounts create vertexai-driver \
  --display-name="Vertex AI Driver"

gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
  --member="serviceAccount:vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"

gcloud iam service-accounts keys create key.json \
  --iam-account=vertexai-driver@YOUR_PROJECT_ID.iam.gserviceaccount.com

export GOOGLE_APPLICATION_CREDENTIALS="/path/to/key.json"
```

## Usage

```go
import "github.com/reliant-labs/reliant/internal/llm/models"

preferences := models.Preferences{
    {ModelID: models.VertexGemini25Flash},
}

// With custom settings
preferences := models.Preferences{
    {
        ModelID:     models.VertexGemini25Pro,
        Temperature: models.TempMedium(),
        TokenBudget: genai.Ptr(int64(8000)),
    },
}
```

Safety levels via `VERTEXAI_SAFETY_LEVEL`: `"low"`, `"medium"` (default), `"high"`.

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

## Limitations

1. **Claude support** not yet implemented
2. **Prompt caching** not yet implemented for Gemini
3. **Extended thinking** not yet supported

## Troubleshooting

### "VERTEXAI_PROJECT environment variable is required"
```bash
export VERTEXAI_PROJECT="your-project-id"
```

### "Failed to initialize Gemini client"
```bash
gcloud auth application-default login
```

### "Permission denied"
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
export VERTEXAI_PROJECT="your-test-project"
export VERTEXAI_LOCATION="us-central1"
go test ./internal/llm/drivers/vertexai/...
```

### Adding New Models

1. Add model constants to `internal/llm/models/vertexai.go`
2. Update the `SupportedModels` array in `internal/llm/drivers/vertexai/models.go`
3. Register the model in the init function

## References

- [Vertex AI Documentation](https://cloud.google.com/vertex-ai/docs)
- [Google GenAI Go SDK](https://pkg.go.dev/google.golang.org/genai)
- [Vertex AI Model Garden](https://cloud.google.com/vertex-ai/docs/model-garden)
- [Anthropic on Vertex AI](https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/claude)
