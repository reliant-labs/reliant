package tdd_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"
)

// Pretend Service interface (mimics what's in a contract.go).
type fakeService interface {
	Lookup(ctx context.Context, id string) (string, error)
	Count(ctx context.Context) (int, error)
}

// In-memory impl.
type fakeImpl struct {
	store map[string]string
}

func (f *fakeImpl) Lookup(_ context.Context, id string) (string, error) {
	if v, ok := f.store[id]; ok {
		return v, nil
	}
	return "", fmt.Errorf("lookup %q: %w", id, errNotFound)
}

func (f *fakeImpl) Count(_ context.Context) (int, error) {
	return len(f.store), nil
}

var errNotFound = errors.New("not found")

func TestTableContract(t *testing.T) {
	svc := fakeService(&fakeImpl{store: map[string]string{"a": "alpha"}})
	ctx := context.Background()

	cases := []tdd.ContractCase{
		{
			Name: "Lookup hit",
			Call: func() (any, error) { return svc.Lookup(ctx, "a") },
			Want: "alpha",
		},
		{
			Name:    "Lookup miss → wrapped errNotFound",
			Call:    func() (any, error) { return svc.Lookup(ctx, "missing") },
			WantErr: errNotFound,
		},
		{
			Name: "Count uses Check",
			Call: func() (any, error) { return svc.Count(ctx) },
			Check: func(t *testing.T, got any) {
				if got.(int) != 1 {
					t.Fatalf("count = %v, want 1", got)
				}
			},
		},
	}

	tdd.TableContract(t, svc, cases)
}

func TestTableContract_SetupRuns(t *testing.T) {
	svc := fakeService(&fakeImpl{store: map[string]string{}})
	ctx := context.Background()
	var setupCalls int

	tdd.TableContract(t, svc, []tdd.ContractCase{
		{
			Name:  "row",
			Call:  func() (any, error) { return svc.Count(ctx) },
			Setup: func(_ *testing.T) { setupCalls++ },
			Want:  0,
		},
	})
	if setupCalls != 1 {
		t.Fatalf("setup ran %d times, want 1", setupCalls)
	}
}
