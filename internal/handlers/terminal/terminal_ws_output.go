// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// wsOutputFrameMax caps the PTY bytes one "output" frame carries. The daemon
// reads the PTY 4 KiB at a time, and writing one frame per chunk made a burst
// (a build log, `cat` of a large file) cost the browser link one websocket
// write per 4 KiB: a slow link or a busy tab fell behind, and before the
// output hops were made lossless that is where bytes were dropped. The pump
// coalesces every chunk already queued into one frame up to this size.
const wsOutputFrameMax = 64 * 1024

// isOutputData reports whether evt carries PTY bytes and nothing else.
func isOutputData(evt *toolexec.TerminalOutputEvent) bool {
	return evt != nil && evt.Error == "" && !evt.Closed && !evt.Overflow
}

// coalesceOutput appends to data the PTY bytes of every data event already
// queued on ch, in order, without waiting, until the frame would pass max. It
// returns the frame, the event it read and did not take (a lifecycle event,
// or a chunk that would have passed max), and whether ch was found closed.
//
// A single chunk larger than max is never split: the caller passes it as
// data, and the frame is that chunk alone.
func coalesceOutput(data []byte, ch <-chan *toolexec.TerminalOutputEvent, max int) (frame []byte, next *toolexec.TerminalOutputEvent, closed bool) {
	for len(data) < max {
		select {
		case evt, ok := <-ch:
			if !ok {
				return data, nil, true
			}
			if !isOutputData(evt) || len(data)+len(evt.Data) > max {
				return data, evt, false
			}
			data = append(data, evt.Data...)
		default:
			return data, nil, false
		}
	}
	return data, nil, false
}

// splitIncompleteUTF8 splits b before a trailing UTF-8 sequence that is cut
// short — the start of a character whose remaining bytes are in the next
// chunk. A frame is a JSON string, and encoding/json replaces each byte of a
// cut character with U+FFFD, so a character split across two PTY reads used
// to reach the browser as two replacement characters. The held-back bytes go
// at the front of the next frame. Bytes that can never become valid UTF-8
// are not held: they pass through as before.
func splitIncompleteUTF8(b []byte) (complete, rest []byte) {
	// A UTF-8 sequence is at most utf8.UTFMax bytes, so only the last
	// UTFMax-1 bytes can be the start of an incomplete one.
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		c := b[len(b)-i]
		if utf8.RuneStart(c) {
			if c < utf8.RuneSelf {
				return b, nil // ASCII: nothing is cut
			}
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i], b[len(b)-i:]
			}
			return b, nil
		}
	}
	return b, nil
}
