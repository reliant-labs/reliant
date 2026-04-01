package compat

import "testing"

func TestBuildAttemptPlan(t *testing.T) {
	attempts := BuildAttemptPlan(map[string]interface{}{"foo": "bar"}, "")
	if len(attempts) == 0 {
		t.Fatal("expected attempts")
	}
	if attempts[0].Name != EnvelopeDirect {
		t.Fatalf("expected direct first got %s", attempts[0].Name)
	}

	explicit := BuildAttemptPlan(map[string]interface{}{"params": map[string]interface{}{"foo": "bar"}}, "")
	if len(explicit) != 1 || explicit[0].Name != EnvelopeDirect {
		t.Fatalf("expected explicit envelope pass-through, got %#v", explicit)
	}
}
