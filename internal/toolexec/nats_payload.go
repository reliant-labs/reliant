// Copyright (c) 2025 Reliant Labs
package toolexec

import "fmt"

// NATS enforces a per-message max_payload (default 1 MB). A publish that
// exceeds it fails client-side — and for a request/reply RESPONSE that
// failure is invisible to the requester, which just waits out its full
// timeout (observed in production: a 5.4 MB fs.search reply against the 1 MB
// limit surfaced as a bare "nats: timeout"). The helpers here implement the
// payload-size protection: preflight every request/reply against the
// connection's actual limit and, on the reply side, substitute a small
// structured ERROR reply in the same envelope format so the caller fails
// fast with an actionable message instead of timing out.

// natsPayloadHeadroom is a safety margin subtracted from the connection's
// max_payload when preflighting a publish. NATS counts headers + payload
// against max_payload; replies carry no headers today, but the margin keeps
// us from publishing right at the edge (e.g. if tracing propagation ever
// adds headers).
const natsPayloadHeadroom = 8 * 1024

// Remediation hints appended to oversize-payload errors. The reply-side hint
// is what an LLM sees as its tool result, so it must tell the model how to
// recover; the request-side hint targets callers pushing large inputs.
const (
	oversizeReplyHint   = "narrow your search or request less data (add filters, lower limits, or target specific paths)"
	oversizeRequestHint = "send less data in a single call (split large payloads into smaller chunks)"
)

// exceedsNATSPayloadLimit reports whether a message of size bytes would
// exceed the connection's max_payload, leaving natsPayloadHeadroom of margin.
// maxPayload <= 0 means the limit is unknown (e.g. connection not yet
// established) — in that case we don't preflight and let the NATS client
// library enforce the limit at publish time.
func exceedsNATSPayloadLimit(size int, maxPayload int64) bool {
	if maxPayload <= 0 {
		return false
	}
	limit := maxPayload - natsPayloadHeadroom
	if limit <= 0 {
		limit = maxPayload
	}
	return int64(size) > limit
}

// oversizeNATSPayloadError builds the caller-facing message for a payload
// that cannot transit NATS. It is deliberately actionable: LLM tool calls
// surface it as the tool result (so the model can react by narrowing its
// query) and user-initiated RPCs return it as the gRPC error message.
// what names the payload (e.g. "response", "tool result", "request"); hint
// is one of oversizeReplyHint / oversizeRequestHint.
func oversizeNATSPayloadError(what string, size int, maxPayload int64, hint string) string {
	return fmt.Sprintf("%s too large (%s exceeds the %s transport limit) — %s",
		what, formatByteSize(int64(size)), formatByteSize(maxPayload), hint)
}

// formatByteSize renders n as a human-readable size (e.g. "5.2 MB").
func formatByteSize(n int64) string {
	const (
		kb = 1 << 10
		mb = 1 << 20
		gb = 1 << 30
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
