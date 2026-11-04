// Copyright (c) 2025 Reliant Labs
package message

import "fmt"

const (
	// MaxReplayedToolMediaParts is how many tool-result images and documents a
	// prompt replays. A tool result's media is durable (an attachment row the
	// history reload folds back into ToolResult.BinaryParts), so without a cap
	// every screenshot an agent ever took rides along on every later turn.
	// That is not only cost: providers reject requests past a media budget —
	// Anthropic refuses more than 100 images, and past 20 shrinks the per-image
	// limit to 2000px, which a full-page screenshot exceeds — and a rejected
	// request would be rebuilt from the same history on every retry, wedging
	// the chat. Ten keeps the screenshots an agent is actively comparing.
	MaxReplayedToolMediaParts = 10

	// MaxReplayedToolMediaBytes bounds the raw bytes of replayed tool media.
	// Base64 inflates by 4/3, so 15MB raw is ~20MB on the wire: inside
	// Anthropic's 32MB request ceiling with room for the conversation itself.
	MaxReplayedToolMediaBytes = 15 * 1024 * 1024

	// droppedToolMediaNote is appended to a tool result whose media was left
	// out of the prompt, so the model knows the result once carried an image
	// and how to get it back.
	droppedToolMediaNote = "\n<system>%d attachment(s) from this tool result are no longer shown, to keep the request within the provider's media limits. Call the tool again to see them.</system>"
)

// LimitToolResultMedia keeps the media of the NEWEST tool results, within
// maxParts parts and maxBytes raw bytes, and strips it from older ones. A
// stripped result keeps its text and gains a note saying what was dropped.
//
// It walks newest to oldest and admits nothing after the first part that does
// not fit, so what survives is always one contiguous recent window: an older
// small image is never kept while a newer large one is dropped, which would
// show the model a stale screen as if it were the current one.
//
// The input is not modified; a message whose results change is copied.
// User-attached media is untouched: it is one upload the user chose to send,
// not an accumulation of per-call results.
func LimitToolResultMedia(msgs []Message, maxParts, maxBytes int) []Message {
	parts, size := 0, 0
	full, copied := false, false
	out := msgs
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != Tool {
			continue
		}
		var newParts []ContentPart
		for j := len(msgs[i].Parts) - 1; j >= 0; j-- {
			tr, ok := msgs[i].Parts[j].(ToolResult)
			if !ok || len(tr.BinaryParts) == 0 {
				continue
			}
			// The newest parts of a result are its last ones.
			admitted := 0
			for k := len(tr.BinaryParts) - 1; k >= 0 && !full; k-- {
				n := len(tr.BinaryParts[k].Data)
				if parts+1 > maxParts || size+n > maxBytes {
					full = true
					break
				}
				parts++
				size += n
				admitted++
			}
			dropped := len(tr.BinaryParts) - admitted
			if dropped == 0 {
				continue
			}
			if newParts == nil {
				newParts = append([]ContentPart(nil), msgs[i].Parts...)
			}
			trimmed := tr
			trimmed.BinaryParts = nil
			if admitted > 0 {
				trimmed.BinaryParts = append([]BinaryContent(nil), tr.BinaryParts[dropped:]...)
			}
			trimmed.Content = tr.Content + fmt.Sprintf(droppedToolMediaNote, dropped)
			newParts[j] = trimmed
		}
		if newParts != nil {
			if !copied {
				out = append([]Message(nil), msgs...)
				copied = true
			}
			out[i].Parts = newParts
		}
	}
	return out
}
