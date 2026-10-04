# LiteLLM: Anthropic prompt caching with 1h TTL through the OpenAI `/chat/completions` endpoint

What this covers: our Go client → LiteLLM proxy (`ghcr.io/berriai/litellm:main-stable`) → `vertex_ai/claude-*`.
Source read: the BerriAI/litellm `main` branch, fetched from raw.githubusercontent.com and a sparse clone. Fetched 2025-10-03 per the local clock.
**Caveat:** `main-stable` can lag `main`. Every line cited here should be checked again against the tag that is actually deployed.

Legend: **[V]** verified from source or docs, **[I]** inferred.

## 1. Where `cache_control` is accepted (OpenAI format → Anthropic)

- **(a) Content part in `messages[].content[]`** [V]. In `litellm/litellm_core_utils/prompt_templates/factory.py` (`anthropic_messages_pt`), each text part goes through `add_cache_control_to_content`:
  ```python
  cache_control_param: Final = original_content_element.get("cache_control")
  if cache_control_param is not None and isinstance(cache_control_param, dict):
      transformed_param: Final = ChatCompletionCachedContent(**cache_control_param)
      anthropic_content_element["cache_control"] = transformed_param
  ```
- **(b) System message part** [V]. In `litellm/llms/anthropic/chat/transformation.py` (system translation, ~L1718/1738):
  ```python
  if "cache_control" in _content:
      anthropic_system_message_content["cache_control"] = _content["cache_control"]
  ```
- **(c) Tool result (`role:"tool"`)** [V]. LiteLLM reads a **message-level** `cache_control` key and puts it on the `tool_result` block. Array content is not required. From `factory.py` (`convert_to_anthropic_tool_result`):
  ```python
  cache_control: Final = message.get("cache_control", None)
  ...
  if cache_control is not None:
      anthropic_tool_result["cache_control"] = cache_control
  ```
  Text parts inside an array-content tool message also keep their own `cache_control` (~L1628: `if cache_control_value is not None: text_content["cache_control"] = cache_control_value`). The two placements land at different levels: message-level → on the `tool_result` block, part-level → on a text block inside it.
- **(d) Tool definitions** [V]. Both placements work. Top level takes precedence. From `transformation.py` (`_map_tool_helper`, ~L849):
  ```python
  _cache_control: Final = tool.get("cache_control", None)
  _cache_control_function: Final = tool.get("function", {}).get("cache_control", None)
  ...
  if _cache_control is not None:
      returned_tool["cache_control"] = _cache_control
  elif _cache_control_function is not None and isinstance(_cache_control_function, dict):
      returned_tool["cache_control"] = ChatCompletionCachedContent(**_cache_control_function)
  ```
- **Message-level `cache_control` on a string-content message** [V/I]. LiteLLM's own injection hook writes `message["cache_control"] = control` when the content is a string (`integrations/anthropic_cache_control_hook.py`, `_safe_insert_cache_control_in_message`), so LiteLLM handles that shape. **[I]** I did not trace how user/system string messages are converted. Prefer array content parts for those.

## 2. Is `ttl` preserved? Beta header? Vertex?

- [V] The TypedDict allows `ttl`. `litellm/types/llms/openai.py`:
  ```python
  class ChatCompletionCachedContent(TypedDict):
      type: Literal["ephemeral"]
      ttl: NotRequired[Literal["5m", "1h"]]
  ```
  `ChatCompletionCachedContent(**d)` is a plain dict constructor, so `ttl` survives. The system, tool-result and top-level tool paths copy the dict verbatim.
- [V] The only code that strips `ttl` is `normalize_cache_control_in_anthropic_payload` in `llms/anthropic/common_utils.py`. Its docstring says: *"strict non-Anthropic implementations … reject … (`cache_control.ttl: 1h is not supported`)"*. Only `llms/snowflake/...` and `llms/openai_like/messages/...` call it. It is **not** on the `anthropic/` or `vertex_ai/` Claude paths.
- [V] Vertex Claude (`llms/vertex_ai/vertex_ai_partner_models/anthropic/transformation.py`) calls `super().transform_request(...)`, which is the Anthropic config, and then only does `data.pop("model")` plus output sanitizing. It does not filter cache_control. So `ttl` reaches Vertex as-is.
- [V] **No auto `extended-cache-ttl-2025-04-11` header.** Searching the whole `litellm/` tree for `extended-cache` returns nothing. `get_anthropic_beta_list` says: *"Anthropic no longer requires the prompt-caching beta header. Prompt caching now works automatically when cache_control is used in messages."* [I] Anthropic made 1h TTL GA, so the header is not needed on either the first-party API or Vertex. Adding it could get the request rejected on Vertex as an unknown beta, so don't send it. If you must, `extra_headers["anthropic-beta"]` is merged into the Vertex beta set (L135–148).
- [I] I found no current LiteLLM code that rejects `ttl` for Vertex. Vertex documents 1h caching for Claude models. If an older pinned image 400s on it, the cause is that image's version, not the request shape.

## 3. Usage reporting and cost

- [V] `AnthropicConfig.calculate_usage` (`transformation.py` ~L2376–2455) builds the following:
  - `prompt_tokens` = input + cache_creation + cache_read. Cache tokens are **included** in prompt_tokens.
  - `prompt_tokens_details.cached_tokens = cache_read_input_tokens`, plus `cache_creation_tokens` and `cache_creation_token_details` (`ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens` from Anthropic's `usage.cache_creation`).
  - Top-level `usage.cache_creation_input_tokens` and `usage.cache_read_input_tokens`.
- [V] Cost for 1h writes: `litellm_core_utils/llm_cost_calc/utils.py` reads `cache_creation_input_token_cost_above_1hr` (L395, L662) and prices the 1h portion with it. In `model_prices_and_context_window.json`, `vertex_ai/claude-opus-4-5` has `cache_creation_input_token_cost: 6.25e-06` and `cache_creation_input_token_cost_above_1hr: 1e-05`. That is 2× the 5e-06 base input price. [I] A model with no `_above_1hr` key (e.g. a new `claude-opus-5-5` entry) falls back to the 5m rate and underprices 1h writes. Check the price map for our model names.

## 4. Proxy-side `cache_control_injection_points`

[V] Schema (`types/integrations/anthropic_cache_control_hook.py`):
```python
class CacheControlMessageInjectionPoint(TypedDict):
    location: Literal["message"]
    role: Literal["user", "system", "assistant"] | None
    index: int | str | None
    control: ChatCompletionCachedContent | None
```
- [V] Negative index is supported: `if targetted_index < 0: targetted_index += len(messages)`. `index` takes precedence over `role`.
- [V] `control` can carry `{"type":"ephemeral","ttl":"1h"}`. The default control also honors `litellm.anthropic_prompt_caching_ttl` ("5m" or "1h").
- [V] The hook caps breakpoints at 4, skips messages that already have cache_control, and puts the mark on the last content part (or on the message itself when content is a string).
- There is no `role: "tool"` target in the type, and tools have no location (`tool_config` is Bedrock-only). That makes client-side marking more precise.

Example `litellm_params`:
```yaml
cache_control_injection_points:
  - {location: message, role: system, control: {type: ephemeral, ttl: "1h"}}
  - {location: message, index: -1,   control: {type: ephemeral, ttl: "1h"}}
```

## 5. Sending cache_control to non-Claude models

- [V] OpenAI: `llms/openai/chat/gpt_transformation.py` `remove_cache_control_flag_from_messages_and_tools` strips it for real openai.com hosts. It is kept only for custom OpenAI-compatible api_bases.
- [I] Gemini/Vertex-Gemini: not traced. LiteLLM translates OpenAI messages into Gemini `contents`, which has no `cache_control` field, so the key is probably dropped. Gating on `claude-*` is still correct, as planned.

## Recommended wire shape (OpenAI request to LiteLLM)

```jsonc
// system
{"role":"system","content":[{"type":"text","text":"...","cache_control":{"type":"ephemeral","ttl":"1h"}}]}
// last user/assistant message: mark the LAST part
{"role":"user","content":[{"type":"text","text":"...","cache_control":{"type":"ephemeral","ttl":"1h"}}]}
// tool result: message-level key (string content is fine)
{"role":"tool","tool_call_id":"call_1","content":"...","cache_control":{"type":"ephemeral","ttl":"1h"}}
// tool definition: top-level on the LAST tool
{"type":"function","function":{...},"cache_control":{"type":"ephemeral","ttl":"1h"}}
```
Use at most 4 breakpoints in total. Anthropic requires 1h breakpoints to come before 5m ones, so use 1h everywhere. Do not send an anthropic-beta header.
