package triggerspec

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"strings"
	"testing"
)

func TestConcurrencyDeclarationAndBoundedKey(t *testing.T) {
	c := &reliantv1.WorkflowConcurrency{MaxRunning: 6, Key: "'repo:42'", Supersede: "latest"}
	if err := CompileConcurrency(c); err != nil {
		t.Fatal(err)
	}
	key, err := ConcurrencyKey(c.Key, Scope{})
	if err != nil || key != "repo:42" {
		t.Fatalf("key=%q %v", key, err)
	}
	for _, expr := range []string{"''", "1", "'" + strings.Repeat("x", 513) + "'", "trigger.payload.missing"} {
		if _, err := ConcurrencyKey(expr, Scope{}); err == nil {
			t.Fatalf("accepted invalid key %s", expr)
		}
	}
	c.MaxRunning = 0
	if CompileConcurrency(c) == nil {
		t.Fatal("zero capacity accepted")
	}
	c.MaxRunning = 6
	c.Supersede = "cancel"
	if CompileConcurrency(c) == nil {
		t.Fatal("unknown policy accepted")
	}
}
