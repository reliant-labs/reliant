# Reading a slow web chat-open trace

Compare the server's `chat_stream.*` spans with the existing `stream_requested`,
`snapshot_received` / `replay_started`, and `caught_up` events on `chat.open`.
The web client also records these events, for at most three stream attempts on
that recording `chat.open`:

| Event | What it tells you |
| --- | --- |
| `stream_headers_received` | Connect made successful response headers available to the client, before it delivered a decoded frame. `content_encoding` is a fixed enum. `not_visible` means absent **or** hidden by CORS; it does not prove an uncompressed response. |
| `stream_frame_read_started` / `stream_frame_delivered` | The client asked Connect for a frame, then received it decoded, before the chat stream applied it. The first four reads get start events and the first four decoded frames get delivery events. |
| `stream_caught_up_frame_delivered` | Connect delivered CaughtUp, even if it fell after the first four frames. Compare it with `caught_up` to separate decoded delivery from the app's subsequent handling. |

Events carry an attempt ordinal and, where relevant, a frame ordinal. On a
delivery event, `read_wait_ms` is the time the read was pending, counted from
headers if the read started earlier. If the wait is short but the gap since
the previous delivery is long, look before the read (the app may have been
processing the previous frame). If the wait is long, it still combines browser
body availability, Connect framing/Protobuf decoding, and scheduling. It does
not locate the delay inside that combined interval.

The existing Fetch span may include Resource Timing such as `responseStart`
when the browser exposes it and the stream finishes. That does not supply
first-chunk or inter-chunk timing on an open stream. The new events don't
read or wrap the response body. A Fetch body probe would see bytes after
browser HTTP content decoding, not the compressed wire chunks, and would add
another forwarding hop; it is deliberately not installed. No payloads,
URLs, tokens, or new identifiers are captured.
