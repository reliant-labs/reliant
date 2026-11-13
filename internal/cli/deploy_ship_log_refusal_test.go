package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// A refusal decided before anything was sent changed nothing, so the ship log
// must not report the stage as live; a real mid-stage failure still is.
func TestShipLog_RefusalBeforeAnyWriteIsNotPartlyShipped(t *testing.T) {
	var l shipLog
	refused := fmt.Errorf("deploy k8s-cluster: %w", cluster.ErrNothingApplied)
	if err := l.run([]string{"[k8s-cluster] hub"}, func() error { return refused }); !errors.Is(err, cluster.ErrNothingApplied) {
		t.Fatalf("the error must pass through, got %v", err)
	}
	if l.changedAnything() {
		t.Fatalf("a refusal that applied nothing was recorded as shipped/partial: %v %v", l.Shipped(), l.Partial())
	}
	if err := l.run([]string{"[k8s-cluster] hub"}, func() error { return errors.New("kubectl: boom") }); err == nil {
		t.Fatal("want the failure")
	}
	if len(l.Partial()) != 1 {
		t.Fatalf("a mid-stage failure must still be partial, got %v", l.Partial())
	}
}
