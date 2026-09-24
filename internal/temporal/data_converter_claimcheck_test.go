// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type countingStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
	puts  int
}

func (s *countingStore) Put(_ context.Context, key string, data []byte, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.blobs[key] = bytes.Clone(data)
	return nil
}

func (s *countingStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.blobs[key]; ok {
		return bytes.Clone(d), nil
	}
	return nil, claimcheck.ErrNotFound
}

type bigResult struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

func bigResultActivity(_ context.Context) (bigResult, error) {
	return bigResult{Content: strings.Repeat("tool output line\n", 4096), Meta: map[string]string{"k": "v"}}, nil
}

// bigResultWorkflow returns a small digest of what it received, so the only
// payload above the threshold is the activity result.
func bigResultWorkflow(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	var out bigResult
	if err := workflow.ExecuteActivity(ctx, bigResultActivity).Get(ctx, &out); err != nil {
		return "", err
	}
	return digest(out), nil
}

func digest(r bigResult) string {
	sum := sha256.Sum256([]byte(r.Content + "|" + r.Meta["k"]))
	return hex.EncodeToString(sum[:])
}

// TestFlexibleDataConverterClaimChecksActivityResults runs a real workflow in
// the SDK test environment, which routes activity results through the
// configured DataConverter (encode on completion, decode in the workflow).
func TestFlexibleDataConverterClaimChecksActivityResults(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	store := &countingStore{blobs: map[string][]byte{}}
	env.SetDataConverter(NewFlexibleDataConverter(WithPayloadStore(store)))
	env.RegisterActivity(bigResultActivity)

	env.ExecuteWorkflow(bigResultWorkflow)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var got string
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("result: %v", err)
	}
	want, _ := bigResultActivity(context.Background())
	if got != digest(want) {
		t.Fatalf("workflow saw a different value than the activity returned")
	}
	if store.puts != 1 || len(store.blobs) != 1 {
		t.Fatalf("puts=%d distinct blobs=%d; want the activity result claim-checked exactly once", store.puts, len(store.blobs))
	}
}

func TestFlexibleDataConverterWithoutStoreRejectsReferences(t *testing.T) {
	store := &countingStore{blobs: map[string][]byte{}}
	withStore := NewFlexibleDataConverter(WithPayloadStore(store))
	payload, err := withStore.ToPayload(bigResult{Content: strings.Repeat("x", 64<<10)})
	if err != nil {
		t.Fatalf("ToPayload: %v", err)
	}
	if store.puts != 1 {
		t.Fatalf("puts = %d, want 1", store.puts)
	}

	var out bigResult
	err = NewFlexibleDataConverter().FromPayload(payload, &out)
	if !errors.Is(err, claimcheck.ErrNoStore) {
		t.Fatalf("storeless FromPayload err = %v, want ErrNoStore", err)
	}
	// And the store-backed converter reads it back.
	if err := withStore.FromPayload(payload, &out); err != nil || len(out.Content) != 64<<10 {
		t.Fatalf("FromPayload with store: err=%v len=%d", err, len(out.Content))
	}
}
