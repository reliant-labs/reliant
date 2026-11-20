// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"errors"
	"testing"
)

// TestShouldRetry_Ladder pins the wait before each retry, in milliseconds:
// 2s doubling plus a fixed 20%, no cap.
func TestShouldRetry_Ladder(t *testing.T) {
	g := &GeminiClient{}
	want := []int64{2400, 4800, 9600, 19200, 38400, 76800, 153600, 307200}
	for i, w := range want {
		retry, got, err := g.shouldRetry(i+1, errors.New("Error 503, Status: UNAVAILABLE"))
		if !retry || err != nil || got != w {
			t.Errorf("shouldRetry(%d) = (%v, %d, %v), want (true, %d, nil)", i+1, retry, got, err, w)
		}
	}
	if retry, _, err := g.shouldRetry(len(want)+1, errors.New("Status: UNAVAILABLE")); retry || err == nil {
		t.Errorf("shouldRetry past MaxRetries = (%v, %v), want (false, error)", retry, err)
	}
}
