// Copyright (c) 2025 Reliant Labs
package llm

import "bytes"

// maxPartialLineBuffer bounds the partial-line carry-over between Reads. A
// body with no newlines at all (a large non-SSE JSON response) would otherwise
// grow this without limit. On overflow the scanner reports content and drops
// the buffer, which is the safe direction: it can only ever keep a stream
// alive, never kill a live one.
const maxPartialLineBuffer = 8192

// sseProgressScanner decides whether bytes read off a response body represent
// real progress or merely a keepalive.
//
// This exists because "did the socket deliver bytes" is NOT the same question
// as "is the provider still working", and conflating them cost us 30+ minute
// silent hangs. When a Claude subscription runs out of credit, Anthropic
// accepts the request, returns 200 with headers, and then emits ping frames
// indefinitely while withholding every content event. Every one of those pings
// reset the idle-timeout clock, so the guard designed to catch a dead stream
// sat there watching a dead stream and calling it healthy. Measured in one
// dogfooding run: seven CallLLM activities held for 28-43 minutes, each
// returning success with zero prompt, output and cache tokens.
//
// The scanner is deliberately CONSERVATIVE — it reports content unless the
// bytes are recognizably a keepalive. A non-SSE body (plain JSON from a
// non-streaming completion) matches none of the keepalive shapes, so it counts
// as content and those callers see no behaviour change at all. The failure
// direction is therefore "kept a stream alive slightly too long", never "cut a
// live stream".
//
// Two keepalive shapes are recognized, both from the SSE spec and Anthropic's
// wire format:
//
//	:ping                                    a comment line (spec keepalive)
//	event: ping\ndata: {"type":"ping"}\n\n   a ping EVENT (what Anthropic sends)
//
// The second needs frame state: its `data:` line is only a keepalive because
// of the `event: ping` line above it, and the two can land in different Reads.
type sseProgressScanner struct {
	// partial holds bytes after the last newline in the previous chunk, so a
	// line split across two Reads is still classified as one line.
	partial []byte
	// inPingFrame records that the current SSE frame was introduced by an
	// `event: ping` line, so its data lines are keepalive too. Cleared by the
	// blank line that terminates every frame.
	inPingFrame bool
}

// sawContent reports whether chunk carried anything other than keepalives.
// It must be called for EVERY chunk read, including ones it reports false for,
// because it carries partial-line and frame state between calls.
func (s *sseProgressScanner) sawContent(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}

	buf := chunk
	if len(s.partial) > 0 {
		buf = append(s.partial, chunk...)
		s.partial = nil
	}

	content := false
	for {
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			break
		}
		if s.lineIsContent(buf[:idx]) {
			content = true
		}
		buf = buf[idx+1:]
	}

	// Whatever follows the last newline is an incomplete line: hold it until
	// the rest arrives rather than classifying a fragment.
	if len(buf) > maxPartialLineBuffer {
		// Not line-oriented (or a pathologically long line). Treat as content
		// and stop buffering — see maxPartialLineBuffer.
		s.partial = nil
		return true
	}
	if len(buf) > 0 {
		s.partial = append([]byte(nil), buf...)
	}

	return content
}

// lineIsContent classifies ONE complete SSE line and advances frame state.
func (s *sseProgressScanner) lineIsContent(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte("\r"))

	// A blank line terminates the current frame.
	if len(line) == 0 {
		s.inPingFrame = false
		return false
	}

	// SSE comment — the spec's keepalive.
	if line[0] == ':' {
		return false
	}

	if rest, ok := cutSSEField(line, "event:"); ok {
		// `event: ping` opens a keepalive frame; any other event names real
		// stream progress (message_start, content_block_delta, ...).
		if string(rest) == "ping" {
			s.inPingFrame = true
			return false
		}
		s.inPingFrame = false
		return true
	}

	if _, ok := cutSSEField(line, "data:"); ok {
		// Only a keepalive when the enclosing frame is a ping frame.
		return !s.inPingFrame
	}

	// Anything unrecognized (including non-SSE payloads) counts as content.
	return true
}

// cutSSEField matches an SSE field name and returns its value with the single
// optional leading space the spec allows. Both `event: ping` and `event:ping`
// are legal on the wire.
func cutSSEField(line []byte, field string) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte(field)) {
		return nil, false
	}
	value := line[len(field):]
	value = bytes.TrimPrefix(value, []byte(" "))
	return bytes.TrimSpace(value), true
}
