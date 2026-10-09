// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// A long agent thread is ~2MB of text across a few hundred messages. The shape
// is computed on every turn, so it has to stay far below the cost of the model
// call it describes.
func BenchmarkComputePromptShape_LongThread(b *testing.B) {
	big := strings.Repeat("x", 8000)
	history := make([]message.Message, 0, 300)
	for i := 0; i < 300; i++ {
		role := message.User
		if i%2 == 1 {
			role = message.Assistant
		}
		history = append(history, textMsg(role, big))
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(big) * len(history)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computePromptShape("m", []string{"sys"}, history, nil)
	}
}
