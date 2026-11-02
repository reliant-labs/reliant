// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/analytics"
)

// allowAll is an analytics.PrivacyChecker: every user has analytics on.
type allowAll struct{}

func (allowAll) GetAnalyticsEnabled(context.Context, string) (bool, error) { return true, nil }

// syncBuffer lets the activity write product events while a test reads them.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// productEventRecorder is a real analytics.Client writing JSON lines to memory,
// the shape a server writes to stdout. trackerFor is what an activity's
// trackerFor field takes.
type productEventRecorder struct {
	out    syncBuffer
	client *analytics.Client
}

func newProductEventRecorder() *productEventRecorder {
	r := &productEventRecorder{}
	r.client = analytics.NewClient(allowAll{}, slog.New(slog.NewJSONHandler(&r.out, nil)))
	return r
}

func (r *productEventRecorder) trackerFor(ctx context.Context, userID string) analytics.Tracker {
	return r.client.ForUser(ctx, userID)
}

// events returns the records written for the named product event, e.g.
// "product.llm_call_completed".
func (r *productEventRecorder) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	r.out.mu.Lock()
	data := append([]byte(nil), r.out.buf.Bytes()...)
	r.out.mu.Unlock()

	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec), sc.Text())
		if rec[analytics.AttrEventName] == name {
			out = append(out, rec)
		}
	}
	return out
}
