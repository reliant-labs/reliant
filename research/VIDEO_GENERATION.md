# Video generation tool (`generate_video`): research and design

Status: design only, with no source edits. Written 2026-10 against the reliant
worktree `small-polish-b5b8ca5e`. It follows up the "Video generation tool"
bullet in `research/MODEL_SETTINGS_PLAN.md` (Wave 5, lines 152-162).

## TL;DR: recommendation

1. **Ship Veo 3.1 first, through `Models.GenerateVideos`, which genai v1.71 already has.** Add Omni Flash second, behind genai **v1.72**, which adds `client.Interactions`.
2. **Default selector: `tags: [video-gen, flagship]`, resolving to `gemini-omni-1.1-flash` once its client exists.** Until then it resolves to `veo-3.1-generate-preview`/`-001`. This reconciles "best and newest" with Google's advice: Omni is the newest model, the only stable (GA) one on the Gemini API, and the one that can be edited conversationally, which is the agent's iterate loop. Veo stays selectable with `cinematic`, and is reached automatically when the user has only a Vertex credential (Omni's Interactions API is not on Vertex).
3. **Execution: block inside the tool.** The generic activity wrapper already heartbeats every activity, and the step `StartToCloseTimeout` is 30 days. On top of that, make the provider operation **resumable**: persist the operation name or interaction id under the tool call id before polling. A re-dispatched activity then resumes polling instead of paying for a second clip. Do not build an async callback system or a dedicated activity for v1.
4. **Storage:** the attachments table (`bytea`). Raise the generated-artifact cap to about 50MB. Add a `video` attachment type, and a `<video>` renderer next to `MessageGeneratedImages`.
5. **What the model gets back:** text only. That means the attachment id, the model, duration, resolution, the prompt as sent, and an **edit handle**. The tool returns no BinaryParts, because no chat model we route takes generated video back usefully.
6. **Credentials:** a user's `gemini` key goes to AI Studio. Platform Vertex goes through a new control-plane metered route (`POST /v1/videos/generations`), because LiteLLM has no video route. That route is the managed path, and it is a separate, later PR.
7. **Live-verified:** a Vertex `veo-3.1-lite-generate-001` call (4s, 720p, no audio, about $0.20) completed in **25.9s**. It returned **inline `bytesBase64Encoded`**, a 498,536-byte mp4, with no GCS bucket required. Details are in §6.

---

## 1. Model lineup

Sources:
- [Omni guide](https://ai.google.dev/gemini-api/docs/omni) (G-OMNI)
- [Omni model card](https://ai.google.dev/gemini-api/docs/models/gemini-omni-flash) (G-OMNI-CARD, last updated 2026-08-27)
- [Veo guide](https://ai.google.dev/gemini-api/docs/veo) (G-VEO)
- [Pricing](https://ai.google.dev/gemini-api/docs/pricing) (G-PRICE)
- [Interactions API](https://ai.google.dev/gemini-api/docs/interactions) (G-INT)
- [Vertex Veo 3.1](https://cloud.google.com/vertex-ai/generative-ai/docs/models/veo/3-1-generate) (V-VEO)
- [Vertex Google models list](https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/google-models) (V-LIST)

| | **gemini-omni-1.1-flash** | **veo-3.1-generate-preview** | **veo-3.1-fast-generate-preview** | **veo-3.1-lite-generate-preview** |
|---|---|---|---|---|
| API | Interactions API, `POST /v1beta/interactions` | `:predictLongRunning` + operation poll | same | same |
| Text input | yes | yes (1,024 tokens) | yes | yes |
| Image (first frame / animate) | yes | yes (`image`) | yes | yes |
| First + last frame interpolation | yes (two images in `input`) | yes (`lastFrame`) | yes | yes |
| Reference images | yes, "subject reference" (`task: reference_to_video`) | up to 3 (`referenceImages`) | up to 3 | **n/a** |
| Video input | yes: edit or extend, ≤10s uploaded; video references up to 3 clips × 3s | extension of a Veo-generated video only | extension only | **no** |
| Audio input | **no** ("Uploading audio references is unsupported") | no | no | no |
| Duration | 3–10s (24 FPS) | 4 / 6 / 8s (must be 8 for 1080p/4k, refs, or extension) | 4 / 6 / 8 | 4 / 6 / 8 (8 for 1080p or refs) |
| Resolution | 360p / **720p default** / 1080p (upscaled) / 4K (upscaled) | **720p default** / 1080p / 4k | 720p / 1080p / 4k | 720p / 1080p (**no 4k**) |
| Aspect | 16:9 (default), 9:16 | 16:9 (default), 9:16 | same | same |
| Audio out | yes by default, steered by the prompt; no on/off flag documented | native audio (`generateAudio`) | native | native |
| Negative prompt | **not supported** (write "Do not…" in the prompt) | `negativePrompt` | yes | yes |
| Conversational edit | **yes**: `previous_interaction_id` | no | no | no |
| Extension | yes, 3–10s continuation, append only | +7s, up to 20×, input ≤141s, 720p only | yes | **no** |
| Latency | "varies with duration, resolution and load" (no figure given) | 11s min, 6 min max at peak (G-VEO "Limitations") | lower | lowest; our live 4s 720p call took **25.9s** on Vertex |
| Price (paid tier) | $17.50 per 1M video output tokens, at 5,792 tok/s of 720p, so **≈$0.10/s at 720p**. Input is $1.50/1M. **No free tier.** | **$0.40/s** (720p/1080p), $0.60/s 4k | **$0.10/s** 720p, $0.12 1080p, $0.30 4k | **$0.05/s** 720p, $0.08 1080p |
| Status (Gemini API) | **Stable**: `gemini-omni-1.1-flash`. Preview alias: `gemini-omni-flash-preview`. "generally available on the paid tier" (G-PRICE). | Preview | Preview | Preview |
| AI Studio key | yes | yes | yes | yes |
| Vertex | Listed as **preview** (V-LIST), but the genai v1.72 example says "Interactions API is not yet supported on Vertex AI" (`examples/interactions/stateful.go`). So in practice it is unavailable to us there. | `veo-3.1-generate-001`, **GA**, us-central1 (V-VEO) | `veo-3.1-fast-generate-001`, GA | `veo-3.1-lite-generate-001`, preview, **live-verified** |
| Rate limit | per project tier ([rate limits](https://ai.google.dev/gemini-api/docs/rate-limits)); not listed per model on the static page | Vertex: 50 requests/min/base model, us-central1 (V-VEO) | same | same |
| Region notes | Editing or extending **uploaded** videos is unavailable in EEA/CH/UK. Minors in images are blocked in EEA/CH/UK, and some recognizable people are blocked everywhere. | `personGeneration`: EU/UK/CH/MENA allow only `allow_adult`. Image-to-video, interpolation and reference images are always `allow_adult`. | same | same |
| Retention | Interactions are stored 55 days (paid), 1 day (free); `store=false` disables `previous_interaction_id` (G-INT) | Generated files are kept **2 days** (the extension clock resets on reference) | same | same |
| Watermark | SynthID | SynthID | SynthID | SynthID |

### Default recommendation

- **"Best and newest" and Google's advice agree.** Omni 1.1 Flash is the newest model (model card: August 2026). It is the only *stable* model in the set on the Gemini API, and Google names it the default. Veo 3.1 is "cinematic", and in practice that means 4x the price of Omni's 720p rate ($0.40/s against ≈$0.10/s) for prompt-to-clip work with no conversation.
- **Omni's decisive property is agent-specific.** The agent cannot see the output (§3.5), so it iterates by talking ("make it slower, keep everything else the same"). Only Omni supports that natively, and without a re-render of the prompt from scratch.
- **Tags, not ids**, as `generate_image` does (`generate_image.go:144-155`):
  - `video-gen` goes on all four models.
  - `flagship` goes on Omni.
  - `cinematic` goes on veo-3.1 and fast.
  - `cheap` goes on lite.
  - Definition order breaks ties (the comment at `models.yaml:1685`). Omni comes first, then veo-3.1, fast, and lite.
- **Fallbacks.** Before the Omni client lands (PR 4 below), the Omni definition is simply absent, and `[video-gen, flagship]` falls through to veo-3.1. For a Vertex-only (managed) user, Omni has no `reliant` provider mapping, so the resolver picks veo-3.1 on `reliant`.

## 2. APIs

### 2.1 Veo: `predictLongRunning`

**AI Studio** (G-VEO REST sample):
```
POST https://generativelanguage.googleapis.com/v1beta/models/veo-3.1-generate-preview:predictLongRunning
x-goog-api-key: $KEY
{"instances":[{"prompt":"…","image":{…},"lastFrame":{…},"referenceImages":[…],"video":{…}}],
 "parameters":{"aspectRatio":"16:9","durationSeconds":"8","resolution":"720p","negativePrompt":"…","personGeneration":"allow_all"}}
→ {"name":"models/veo-…/operations/<id>"}
GET  {BASE}/{operation_name}            (poll; SDK sleeps 10s)
→ done:true, response.generateVideoResponse.generatedSamples[0].video.uri
GET  <uri>   with x-goog-api-key, follow redirects  → mp4 bytes
```

The download is a Files API URI that **requires the key header**. The file expires after 2 days. That makes the URI a short-lived handle, never a durable link to give the UI.

**Vertex** (live-verified, §6):
```
POST https://us-central1-aiplatform.googleapis.com/v1/projects/{P}/locations/us-central1/publishers/google/models/veo-3.1-lite-generate-001:predictLongRunning
Authorization: Bearer <ADC/SA token>   (x-goog-user-project for user ADC)
same instances/parameters (+ "sampleCount", "generateAudio", optional "storageUri": "gs://…")
POST …:fetchPredictOperation {"operationName": name}   (poll; NOT GET on the op name)
→ response.videos[0].bytesBase64Encoded + mimeType "video/mp4"
```

Without `storageUri`, Vertex returns **inline base64**. With it, the response carries `gcsUri`. Inline is fine at our sizes: 4s at 720p came to 0.5MB. The 8s 1080p size is unmeasured, but the same bitrate suggests about 2–5MB.

**genai v1.71** already covers both backends:
- `Models.GenerateVideos(ctx, model, prompt, image, *GenerateVideosConfig)` and `GenerateVideosFromSource` (`genai@v1.71.0/models.go:5742,5751`).
- `Operations.GetVideosOperation` is the poll call.
- `GenerateVideosConfig` covers every parameter we need: `DurationSeconds`, `AspectRatio`, `Resolution`, `NegativePrompt`, `GenerateAudio`, `LastFrame`, `ReferenceImages`, `PersonGeneration`, `Seed`, `OutputGCSURI`, `EnhancePrompt`, and `Video` for extension (`types.go:4979-5037`).
- `Files.Download` fetches AI Studio URIs.

Our existing `llm.NewGenAISDKClient` injection, the same one `imagegen.NewGemini` uses (`drivers/image_generation.go:163`), applies unchanged.

### 2.2 Omni: Interactions API

G-OMNI, with REST samples:
```
POST https://generativelanguage.googleapis.com/v1beta/interactions?key=$KEY   (or x-goog-api-key)
{"model":"gemini-omni-1.1-flash",
 "input": "prompt" | [{"type":"image","data":"<b64>","mime_type":"image/png"}, {"type":"text","text":"…"}],
 "previous_interaction_id": "v1_…",                      // follow-up edit
 "response_format": {"type":"video","delivery":"uri","aspect_ratio":"9:16","resolution":"720p"},
 "video_config": {"task":"text_to_video|image_to_video|reference_to_video|edit|extend"}}  // optional; prefer prompting
→ {"id":"v1_…","status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","data":"<b64>" | "uri":"…/files/…:download?alt=media"}]}]}
```

- **Output.** The default is inline base64 in `steps[].content[].data`. `output_video` is an *SDK-only* convenience field. With `delivery:"uri"`, you get a Files URI and poll `GET /v1beta/files/{id}` until `state == ACTIVE` (or `FAILED`), then `GET …:download?alt=media`.
  - **Use `delivery:"uri"`.** It turns a single multi-minute HTTP request into a short create followed by a poll. That gives the same resumable shape as Veo, and it stays clear of `llm.StreamingHTTPClient`'s idle-body timeout.
- **Async.** G-INT also offers `background=true` (create returns at once; poll `GET /interactions/{id}`). That is the cleaner async primitive if `delivery:uri` still blocks the create call for the whole render. The docs do not say which, so measure it in PR 4.
- **Multi-turn.** Send `previous_interaction_id: <prior id>` with a short edit prompt. The server holds the history, *including the generated video*. Per-interaction parameters (`response_format`, etc.) are **not** inherited and must be re-sent (G-INT). This needs `store=true` (the default), and the stored interaction lives 55 days (paid tier) or 1 day (free).
- **Raw HTTP vs SDK.** Raw HTTP is trivial: one JSON POST, one GET. But **genai v1.72.0 exists and ships Interactions**:
  - `go list -m -versions` tops out at v1.72.0.
  - The `interactions/` subpackage, `client.Interactions *interactions.Interactions` (`genai@v1.72.0/client.go:58`), and `Create/Get/Delete/Cancel` (`interactions/interactions.go:52,339,577,861`).
  - The Go sample in G-OMNI uses `interactions.VideoResponseFormat{Delivery: …URI}`.

  Upgrading means a one-line `go.mod` bump from v1.71 to v1.72, a minor version. Re-run the imagegen and gemini driver tests, because the SDK is shared with chat. **Recommendation: bump to v1.72 and use the SDK.** It is a Speakeasy-generated client with typed unions. If it fights our injected `http.Client`, fall back to hand-rolled HTTP in the antigravity style (`drivers/imagegen/antigravity.go`).

## 3. Our architecture (code-read)

### 3.1 How `generate_image` works end to end
1. **Tool.** `internal/llm/tools/generate_image.go`:
   - Params: `:65-76`. `model` is a *bound* `models.ModelSelector`, hidden from the agent by `DefaultBindings` → `tags:[image-gen, flagship]` (`:151-155`).
   - `RequiresPermission` is always true (`:157-161`).
   - `Execute` (`:163-254`) does: validate → user id → `resolve(ctx, userID, selector)` → `GenerateImage` → `storeAttachment` → optional `saveToDaemon` → `NewImageResponse(summary, []BinaryContent)` plus structured metadata `GenerateImageOutput` (`:105-117`).
   - The consumer-side interfaces `ImageGenerator` / `ImageGeneratorResolver` (`:32-46`) avoid an import cycle with `internal/llm/drivers`.
2. **Resolution.** `drivers/image_generation.go`:
   - `ResolveImageGenerator` (`:99-140`) pins `RequireOutputModality = ModalityImage`, a hard filter (`:103`). It gets configured providers from `GetAvailableDrivers`, runs `registry.Resolve`, then builds the client with `imageGenConfig` (`:188-231`).
   - Managed `reliant` goes to `ResolveReliantBaseURL`, the control-plane proxy, which is "the only place image spend is metered" (`:199-209`).
   - `newImageGenClient` switches on **driver**, not model (`:160-172`): gemini → `imagegen.NewGemini` (genai SDK), antigravity → hand-rolled, default → OpenAI-shaped.
3. **Clients.** `drivers/imagegen/{client.go, gemini.go, antigravity.go, retry.go}`. `retry.go` is shared retry/backoff.
4. **Persistence.** `storeAttachment` writes the bytes into `attachments.content` (`bytea`, `internal/db/postgres/schema.sql:137-149`) with `AttachmentType: image` (`generate_image.go:277-300`). The comment there gives the reason: in distributed mode the API server and worker cannot see disk.
5. **Model vs UI.** `message.ToolResult` (`internal/models/message/content.go:190-209`) carries two channels:
   - `BinaryParts` is what the **model** sees, and it lives only for the request.
   - `AttachmentIDs` is what the **user** sees. It is durable and served at `/api/attachments/{id}`.

   `workflow/runtime/save_message.go:479-492` carries `attachment_ids` through CEL. `threads/save_message.go:683-719` materializes one content block per id.
6. **Modality.** `models/modality.go` defines only `ModalityText` and `ModalityImage`. The catalog declares `output_modalities: [image]` per definition (`models.yaml:1663+`). The `image-gen` tag group is at `models.yaml:171`.
7. **UI.**
   - `ChatMessage.tsx:1066-1080` collects `exec.result.attachments` per tool-run segment and renders `MessageGeneratedImages` outside the collapsed tool card.
   - `useAttachmentBlobUrls.ts:28-48` loads **only** `image/*` through `attachmentGrpc.getAttachmentAsBlob`, a whole-blob gRPC `GetAttachment`.
   - **No chat component renders `<video>`.** The only `<video>` hit is `FileBrowser/FileViewerTab.tsx`.

### 3.2 Long-running execution: the three options

Facts:
- **Timeouts.** Step activities run with `StartToCloseTimeout: 30 days` and `HeartbeatTimeout: activityHeartbeatTimeout = 30s` (`step_executor.go:928-972`, `registry.go:323`).
- **Heartbeats.** The generic activity wrapper heartbeats on a ticker for **every** activity, with `elapsed_seconds` in the payload (`registry.go:640-680`). A tool that blocks for 3 minutes is therefore already healthy from Temporal's point of view.
- **Cancellation.** Cancel and interrupt resolve immediately (`WaitForCancellation` unset, `step_executor.go:942-950`). The activity's ctx is cancelled at the next heartbeat (≤3s throttle).
- **Re-dispatch.** It returns the recorded terminal result when one exists (`activities/handlers/execute_tools.go:523`, `checkPriorTerminalResult`). The `:529` comment notes the case it *cannot* cover: a worker that died mid-tool. That tool simply runs again.
- **Background work today.** Shell backgrounding is daemon-side, and the agent *pulls* results with `shell_wait`/`shell_output` (`tools/shell_wait.go`; `grpc/services/tool_call.go:296-320`). Background spawns are child workflows, repaired by the reconciler (`reconciliation/reconciler.go:184-193`). Neither has a push-a-result-into-the-agent primitive that a server-side tool could reuse without new machinery.

| Option | Pros | Cons |
|---|---|---|
| **A. Block in tool + heartbeats** (recommended) | Zero new infrastructure. The result lands in the same turn, so the agent can reason over it immediately. Cancel works via ctx. Matches `generate_image`. | A worker restart mid-render re-runs the tool, which means **double spend**. That is fixed with a resumable operation (below). The turn is "busy" for 30s–6min. |
| B. Async job + post back | The agent can keep working. | Needs a new "inject a tool-ish result into a running or idle thread" path, and wakes an idle chat. It is a whole new subsystem with its own reconciler, and the agent cannot reason about the video until later anyway. |
| C. Dedicated activity | Per-activity timeouts and retry policy. | Every tool already runs under a 30-day/heartbeat activity. A new activity type means workflow-graph plumbing for no gain. |

**Recommended: A + resumable operation.**
- Before the first poll, write `{tool_call_id → provider, operation_name | interaction_id | file_id, model, started_at}`. Store it as tool-call metadata, or in a small `video_generation_jobs` row; there is no down migration, per policy.
- On entry, `Execute` checks for an existing record for `tc.ToolCallID`. If one exists, it **resumes polling instead of submitting**.
- Polling runs every 5–10s, with a deadline of 10 minutes (the documented Veo max is 6). It honours `ctx.Done()`.

Failure modes:
- **Worker restart.** The re-dispatched activity finds the record and resumes the poll. The provider job kept running, so it costs nothing extra.
  - Veo files live 2 days, and Vertex inline results stay on the operation.
- **User cancel or interrupt.** The ctx is cancelled, so polling stops.
  - Veo has no cancel. The clip is still billed if it completes (Google bills only successful generations, per G-PRICE's note).
  - Omni has `Interactions.Cancel` (`interactions.go:861`). Call it best-effort on cancel.
  - The tool records `cancelled`. The next turn does not resume a cancelled job.
- **Quota (429 / RESOURCE_EXHAUSTED).** Retry submission only, with `imagegen/retry.go`-style backoff, at most about 3 tries. Never retry after the job has been accepted. Surface "Video quota exhausted for <provider>; try again later or switch model."
- **Safety block.** A `raiMediaFilteredCount > 0` or empty `videos` becomes a non-retryable error that includes the filter reason when present. It must never be reported as "no video" without a cause.
- **Operation reports `error`.** Return the provider message. Treat `FAILED` file state the same way.

### 3.3 Storage
- **Backend.** Store the bytes in `attachments.content` (`bytea`), the same as images. Postgres handles 50MB rows, but every read of the row pulls the whole value. The attachments read path already selects content only on fetch.
- **Limits today.**
  - User uploads: `AttachmentService.maxFileSize` = 10MB, or the `MAX_UPLOAD_SIZE` env (`grpc/services/attachment.go:34-41`).
  - Web drag-drop: 50MB (`useDragAndDrop.ts:47`, `ChatInput.tsx:1109`).
  - A tool insert goes straight through `repo.CreateAttachment` (`generate_image.go:296`), so the 10MB check **does not apply**. Add an explicit 64MB guard in the tool.
- **Temporal payload.** `ToolResult` must **never** carry the mp4. Claim-check offloads payloads over 32KB but caps decode at 256MB (`temporal/claimcheck/codec.go:61,76`). Carrying 50MB per tool result through history is wrong regardless. Return the attachment id only, with `BinaryParts` empty.
- **Serving.** The UI fetches via gRPC `GetAttachment` → Blob (`web/src/api/attachment-grpc.ts:150-172`). For video, add a **streaming HTTP endpoint with `Range` support**, `GET /api/attachments/{id}/content` using `http.ServeContent`. That lets `<video>` seek without buffering 50MB in JS. The URL string `/api/attachments/%s` already exists (`grpc/services/attachment.go:64`); check whether a byte-serving handler exists, because this read found only the URL formatting.
- **Type.** `attachment.TypeImage`/`TypeDocument` only (`internal/attachment/filetypes.go:21-27`). Add `TypeVideo` with `.mp4`/`.webm`/`.mov` on the Go side and in `web/src/lib/filetypes`.
  - Classification is by extension (`generate_image.go:327-331`), so the filename is `generated-<8>.mp4`.

### 3.4 What the tool returns to the model
Return text only, with structured metadata:
```
Generated generated-1a2b3c4d.mp4 (video/mp4, 8s, 720p 16:9, audio on, 3.1 MB) with gemini-omni-1.1-flash in 47s.
Attachment id: <uuid>
Edit handle: <uuid>   (pass as edit_from to refine this video)
Prompt sent: "<exact prompt>"
You cannot see this video. Describe what you asked for, not what it shows; ask the user to review it.
```
Metadata is `GenerateVideoOutput{AttachmentID, Model, DurationSeconds, Resolution, AspectRatio, Audio, Bytes, Prompt, EditHandle, ProviderJobID, SavedTo}`.

Optionally, extract a **poster frame** and return it as one image BinaryPart, so the model sees *something*. Do not do this in v1: the server has no ffmpeg, and Veo/Omni return no thumbnail. Revisit if users ask "does it look right?"

### 3.5 Multi-turn editing
- **Handle = the attachment id** of the previous video, never a raw provider id. The tool maps attachment → `video_generation_jobs` row → `{provider, interaction_id | veo video ref, model, user_id}`. This keeps provider state out of the agent's vocabulary and enforces ownership: the user id must match.
- **Omni.** `edit_from` → `previous_interaction_id`. Re-send `response_format` (it is not inherited). The interaction lives 55 days on the paid tier and 1 day on free. When it has expired, return "edit handle expired; regenerate with a full prompt".
- **Veo** has no edit. `edit_from` plus `mode: extend` → `GenerateVideosFromSource{Video: ref}` (3.1 and fast only, within 2 days, 720p). Any other edit request on a Veo handle returns an error that suggests regenerating, or switching to Omni.
- **Cross-model.** Uploading our stored mp4 to Omni as an input video works only for clips ≤10s, and **not in EEA/CH/UK**. Keep it as a stretch goal.

## 4. Credential and billing routing

- **User `gemini` key (BYO).** Direct to AI Studio (`imageGenBaseURLs["gemini"]`, `image_generation.go:49-54`), with all four models available. Google bills the user directly, and we meter nothing. Today the dev DB has **no `gemini` api_key row** (5434/reliant `api_keys` providers: reliant 16, codex 6, claude 4, antigravity 1, copilot 1).
- **Managed (`reliant`).** Image spend is metered because the request passes through the control-plane LLM proxy:
  - `control-plane/internal/llmproxy/proxy.go:42-65` mounts exactly `ProxiedPaths = {chat/completions, messages, images/generations}`, and the comment there says "adding a model-invoking endpoint means adding it HERE".
  - `billing.go:74-88,346-350` debits from LiteLLM's `X-Litellm-Response-Cost` header.

  **LiteLLM has no video route, so there is no cost header to read.** Managed video therefore needs a control-plane change:
  1. Add `PathVideosGenerations = "/v1/videos/generations"` (submit) and `GET /v1/videos/operations/{id}` (poll) to `ProxiedPaths`. Use a Reliant-defined JSON shape that mirrors our `videogen.Request`.
  2. The handler calls Vertex directly using platform credentials (SA/WIF; the prod project; `us-central1`). Dev already has ADC in `control-plane-litellm-1` (`VERTEX_PROJECT=reliant-labs-475814`).
  3. **Metering is computed, not reported.**
     - Price is `per_second[model][resolution] × durationSeconds × margin`, taken from a price table next to the model config. Mirror G-PRICE.
     - **Reserve before submit** (the wallet hold path in `internal/billing/svcbilling/canonical_metering.go`). Settle on `done` with videos > 0. Release on failure or safety block, because Google charges only successful generations.
     - Key the reservation by operation name, so poll retries are idempotent.
  4. The poll route returns the mp4 bytes. Either inline them once, or stream them and drop the operation, so the reliant worker stores the attachment.
  5. Omni stays BYO-only until Interactions reaches Vertex. Alternatively, the platform holds an AI Studio paid key; that is a business decision, so ask before doing it.
- The resolver needs no change: `findBestProvider` already prefers BYO over managed on a tie (`image_generation.go:95-98`).

## 5. Design

### 5.1 Tool schema (`generate_video`)
```go
type GenerateVideoParams struct {
  Model          models.ModelSelector `json:"model,omitempty"`   // BOUND: tags [video-gen, flagship]
  Prompt         string   `json:"prompt"`                        // required. Subject, action, camera, style, audio cues.
  DurationSeconds int     `json:"duration_seconds,omitempty"`    // default: provider default; validated per model (Veo 4|6|8, Omni 3-10)
  Resolution     string   `json:"resolution,omitempty"`          // enum 720p|1080p|4k  (default 720p — cost)
  AspectRatio    string   `json:"aspect_ratio,omitempty"`        // enum 16:9|9:16
  StartFrame     string   `json:"start_frame,omitempty"`         // attachment id (image) to animate
  EndFrame       string   `json:"end_frame,omitempty"`           // attachment id (image); requires start_frame (interpolation)
  ReferenceImages []string `json:"reference_images,omitempty"`   // ≤3 attachment ids
  EditFrom       string   `json:"edit_from,omitempty"`           // attachment id of a previously generated video
  Mode           string   `json:"mode,omitempty"`                // enum generate|edit|extend (default: edit if edit_from else generate)
  NegativePrompt string   `json:"negative_prompt,omitempty"`     // Veo native; Omni: appended as "Do not: …"
  Audio          *bool    `json:"audio,omitempty"`               // Veo generateAudio; Omni: prompt "No audio/dialogue" when false
  SaveTo, Repo   string                                          // as generate_image
}
```

**Validation runs in the driver against declared capabilities, not with ad-hoc ifs.** Add to `ModelCapabilities` a `video: {durations, resolutions, aspects, max_reference_images, supports_edit, supports_extend, supports_negative_prompt}` block. Then return actionable errors such as "1080p on veo-3.1 requires duration 8".

`RequiresPermission` is true. The description must state the cost: "≈$0.05–0.60 per second; default 720p; you cannot see the output".

### 5.2 Catalog (`models.yaml`)
```yaml
tags:
  video-gen:
    - {model: gemini-omni-1.1-flash}
    - {model: veo-3.1-generate}
    - {model: veo-3.1-fast-generate}
    - {model: veo-3.1-lite-generate}
models:
  - id: gemini-omni-1.1-flash
    capabilities: {output_modalities: [video], video: {durations: [3..10], resolutions: [360p,720p,1080p,4k], aspects: ["16:9","9:16"], supports_edit: true, supports_extend: true, supports_negative_prompt: false, max_reference_images: 3}}
    tags: [video-gen, flagship]
    providers: [{driver: gemini, api_model: gemini-omni-1.1-flash}]
  - id: veo-3.1-generate
    tags: [video-gen, cinematic]
    providers: [{driver: gemini, api_model: veo-3.1-generate-preview}, {driver: reliant, api_model: veo-3.1-generate-001}]
  - id: veo-3.1-fast-generate   # same shape, api_model veo-3.1-fast-generate-preview / -001
  - id: veo-3.1-lite-generate   # tags [video-gen, cheap]; no refs/extend/4k; -preview / veo-3.1-lite-generate-001
```

- Add `ModalityVideo Modality = "video"` to `models/modality.go`.
- **Gotcha:** keep `visibility` such that video models never appear in the chat model picker. The existing `user_visible_modality_test.go` is the pattern to extend.

### 5.3 Driver layout (mirrors imagegen)
```
internal/llm/drivers/video_generation.go      ResolveVideoGenerator (RequireOutputModality=video), videoGenConfig, newVideoGenClient (switch on driver)
internal/llm/drivers/videogen/
  types.go      Request{Prompt, Duration, Resolution, Aspect, StartFrame, EndFrame []byte+mime, Refs, NegativePrompt, Audio *bool, Edit *EditRef}
                Job{Provider, OperationName|InteractionID|FileID}; Response{Bytes, MIME, ModelID, Driver, Job, Elapsed}
  client.go     interface { Submit(ctx, Request) (Job, error); Poll(ctx, Job) (*Response, done bool, error) }
  veo.go        genai Models.GenerateVideos / Operations.GetVideosOperation / Files.Download (AI Studio)
  omni.go       genai v1.72 Interactions.Create(delivery=uri) + Files.Get/Download; Cancel on ctx.Done
  managed.go    control-plane /v1/videos/* (reliant driver)
  poll.go       shared poll loop: interval, deadline, ctx, error classification (reuse imagegen/retry.go ideas)
internal/llm/tools/generate_video.go          consumer-side VideoGenerator/Resolver interfaces (cycle rule, as generate_image.go:27-46)
```
The Submit/Poll split, rather than a single `Generate`, is what makes resume-after-restart possible.

### 5.4 UI
- `MessageGeneratedVideos.tsx` lives next to `MessageGeneratedImages`. `ChatMessage.tsx:1066` splits `runImages` by MIME type.
- It renders `<video controls preload="metadata" playsInline src="/api/attachments/{id}/content">`, using the Range endpoint and no blob. Add a download button. If the response is 404 or expired, show the error state.
- **Progress while generating.** The tool card is already "running" for the whole call. Add elapsed time plus "Generating video (typically 30s–3min)" in a `GenerateVideoToolRenderer` under `tool-renderers/`.
  - Real progress needs a status stream from the tool. Defer it; neither provider reports a percentage.
- Enable `video/*` uploads in `ChatInput` for `edit_from` in a later phase.

### 5.5 Error handling summary
| Condition | Behaviour |
|---|---|
| No video-capable provider | "Add a Gemini API key in Settings, or enable Reliant credits, to generate video" (same as `image_generation.go:107-108`) |
| Unsupported param for the resolved model | Validation error naming the model, the param, and the allowed values |
| 429 / quota on submit | Bounded backoff, then a clear message. Never resubmit after the job is accepted. |
| Safety filtered | Non-retryable error with the reason. Not billed (managed path releases the hold). |
| Timeout (>10 min) | Error that keeps the job record, so a re-ask can resume it ("still rendering; job id …") |
| Edit handle expired or foreign | "Edit handle unavailable; regenerate with a full prompt" |
| Storage failure after success | Error that includes the provider URI if it is still valid (2 days), so the clip is not silently lost |

### 5.6 Test plan
- **Unit (no network):**
  - `videogen` against `httptest`, replaying recorded JSON for submit/poll/done, filtered, error, and a 429.
  - Both Vertex (`fetchPredictOperation`, inline b64) and AI Studio (`generatedSamples[].video.uri`, then download).
  - Omni `steps[].content[]` with both `data` and `uri`.
  - Resume: a second `Execute` with the same tool call id must call Poll, never Submit. **Write this test first and confirm it fails** without the job record.
  - Capability validation table.
  - Attachment type `.mp4` → video.
- **Tool tests:** mirror `generate_image_test.go`. The `ToolResult` has `AttachmentIDs` and **no `BinaryParts`**. Assert the bytes never appear in the response.
- **Web:** `ChatMessage.generatedVideos.test.tsx` checks that a video attachment renders `<video>` with the content URL.
- **Control-plane:** a metering test (reserve → settle on success, release on filtered or failed) with a fake Vertex.
- **Cheap live test** (build tag `live`, manual):
  - `veo-3.1-lite`, 4s, 720p, `generateAudio:false`, about $0.20. This is exactly the call made in §6.
  - Assert an `ftyp` box and more than 100KB.
  - Omni live test: 3s at 360p, about $0.30, which needs a `gemini` key.

### 5.7 Implementation outline (each step ships independently)
1. **Plumbing.** `ModalityVideo`, the `video` capability block, `TypeVideo` attachment classification, and the Range-capable attachment content endpoint. No user-visible change.
2. **UI player.** `MessageGeneratedVideos` and the tool renderer, tested with a fixture attachment.
3. **Veo via BYO `gemini` key.** The `videogen` package (veo + poll), `ResolveVideoGenerator`, the `generate_video` tool with a resumable job record, and catalog entries for the veo-3.1 family on the `gemini` driver. Default is `[video-gen, flagship]` → veo-3.1.
4. **Omni.** Bump genai to v1.72, add `omni.go`, the catalog entry first in order (it becomes the default), and `edit_from` mapped to `previous_interaction_id`.
5. **Managed video.** The control-plane `/v1/videos/*` route to Vertex, with computed metering, plus `reliant` provider mappings for Veo.
6. **Extras.** Veo extension mode, video upload as `edit_from` input for Omni, and poster-frame BinaryPart.

### 5.8 Devil's advocate
- **"Default to Veo; it's the quality leader."** Veo 3.1 standard costs 4x Omni's 720p rate and cannot iterate conversationally. Its "quality" is unverifiable by an agent that cannot see the output. Omni is also stable while Veo on the Gemini API is preview. The case for Veo is real only for one-shot cinematic work, and `cinematic` covers it. **Risk:** Omni's "a few different shots" default (G-OMNI prompt guide) surprises users who expect one shot. Put "single continuous shot" guidance in the tool description.
- **"Blocking a turn for minutes is bad UX."** It is, but the agent cannot do anything useful with the video until it exists, and cancellation still works. An async path would add a result-injection subsystem we do not have. Revisit if users want to queue several videos; parallel tool calls already allow N concurrent renders.
- **"Resumable jobs are over-engineering."** Without them, each worker deploy during a render repeats a $0.40–3.20 charge, and deploys happen several times a day. The job record is one table and two branches.
- **"bytea for 50MB is wrong; use object storage."** It is the right long-term answer: it matches the claim-check/blob direction and enables CDN delivery. But the attachments table is the single substrate every process can read in distributed mode today (`generate_image.go:273-276`). Start there behind the attachment repo interface, so swapping in GCS later is local.
- **"Depending on genai v1.72 for Omni ties us to a fresh, generated client."** True. Raw HTTP is two endpoints, and the fallback is cheap. The SDK choice is reversible inside `omni.go`.
- **"Managed video without LiteLLM breaks the one-meter rule."** It does not: the meter is the control-plane proxy, not LiteLLM. Computed metering is weaker than a provider-reported cost, so pin the price table in a test against G-PRICE, and reserve before submit so a pricing bug can only under- or over-hold, never bill unbounded.

## 5b. Managed (Reliant credits) video — verified 2026-10-05, DECIDED

Supersedes §4's "computed metering" and §5.7 step 5. User decision: build the
managed path; Veo is enabled in the project's Vertex Model Garden.

Verified live (project reliant-labs-475814, dev LiteLLM `control-plane-litellm-1`):
- **Omni is NOT on Vertex.**
  - `gemini-omni-1.1-flash` returns the same 404 as a made-up model name
    ("Publisher model … not found or your project does not have access"), at
    `global` and `us-central1`.
  - LiteLLM's price table lists Omni only as `gemini/` (AI Studio) and
    `runwayml/`, never `vertex_ai`.
  - So managed = Veo only; Omni stays BYO `gemini`.
- **Veo 3.1 IS on Vertex** (lite, fast, standard). LiteLLM prices them per
  second as `vertex_ai/veo-3.1-{lite,fast,}-generate-001` = $0.05 / $0.10 / $0.40.
- **LiteLLM already serves an OpenAI-style video API** on the running proxy
  (`/openapi.json`):
  - `POST /v1/videos` (create)
  - `GET /v1/videos/{id}` (status)
  - `GET /v1/videos/{id}/content` (bytes)
  - plus `/remix`, `/extensions`, `/edits`
  - The video id is a base64 envelope encoding provider + model + Vertex
    operation name.
- **Cost reporting (one live Veo Lite 4s/720p render via `litellm.video_generation`):**
  - CREATE: `_hidden_params.response_cost = 0.2` (4s × $0.05) and
    `usage.duration_seconds = 4.0`.
  - STATUS: `response_cost = 0.0`.
  - CONTENT: `response_cost = None`.
  - Render completed, 666,997 bytes, valid MP4.
  - So **Google returns no cost, but LiteLLM computes it from its price table
    and reports it on CREATE** — the same cost source control-plane already
    bills every route from.
- **Billing hazard this creates**:
  - The cost is known at submit, but the render can still FAIL after
    acceptance. Verified: a submit with durationSeconds=999 returned 200 with
    an operation, and that operation later finished `done` with error
    "Unsupported output video duration 999 seconds" and produced no video.
  - Billing on the create header would charge for failed renders.

Design (decided):
- **One meter.**
  - Add the `/v1/videos` routes to control-plane `llmproxy.ProxiedPaths`.
  - Add Veo `vertex_ai/...` entries to LiteLLM's generated model_list (via
    the generator, `task generate:llm-gateway`, from reliant's catalog
    `reliant` provider mappings).
  - No hand-written price table: the cost is LiteLLM's.
- **Settlement for video (reserve → settle → release):**
  - On a 2xx CREATE, place a HOLD for the create-time `response_cost`, keyed
    by the video id.
  - On a STATUS response with status `completed`, settle the hold to a
    debit, exactly once (idempotent per video id).
  - On `failed`, or an expired hold, release it.
  - Status and content calls are never billed themselves.
  - Pre-submit balance check (`creditExhaustedJSON`) applies as for other
    routes.
- **Reliant:**
  - `videogen/managed.go` speaks the OpenAI video API to the `reliant` driver
    base URL.
  - `reliant` provider mappings for the three Veo models.
  - Remove the `withoutManagedDrivers` exclusion.
  - Credits default: standard falls back to veo-3.1-FAST (not veo-3.1) when
    Omni is unavailable, to avoid silently defaulting credit users to the
    most expensive model.

## 6. Live verification (one clip)
- **Credential check.**
  - 5434/reliant `api_keys` has no `gemini` row.
  - `control-plane-litellm-1` has `GOOGLE_APPLICATION_CREDENTIALS` (authorized_user ADC) and `VERTEX_PROJECT=reliant-labs-475814`.
  - The token was used inside the container and never printed.
- **Request.** `POST …/publishers/google/models/veo-3.1-lite-generate-001:predictLongRunning` with:
  - `instances: [{prompt: "A red ball bouncing once on a wooden floor, single continuous shot."}]`
  - `parameters: {durationSeconds:4, resolution:"720p", aspectRatio:"16:9", sampleCount:1, generateAudio:false}`
- **Submit** returned HTTP 200 with `{"name": "projects/…/models/veo-3.1-lite-generate-001/operations/<uuid>"}`.
- **Poll.** `POST …:fetchPredictOperation {"operationName": name}`. The operation was `done` at **25.9s**.
- **Response:**
  ```
  {"@type":"type.googleapis.com/cloud.ai.large_models.vision.GenerateVideoResponse",
   "raiMediaFilteredCount":0,
   "videos":[{"bytesBase64Encoded":"<664,716 chars>","mimeType":"video/mp4"}]}
  ```
  The payload decoded to **498,536 bytes** with an `ftyp` box. Inline bytes need no GCS bucket. Expected cost: 4s × $0.05 = $0.20.
- Not verified live: Omni (no `gemini` key) and AI Studio Veo.
