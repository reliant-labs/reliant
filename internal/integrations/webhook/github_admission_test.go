package webhook

import (
	"strings"
	"testing"
)

func TestObservedProviderHeadIsNotChronological(t *testing.T) {
	payload := func(sha string) map[string]any {
		return map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": sha}}}
	}
	newer, older := strings.Repeat("b", 40), strings.Repeat("a", 40)
	if observedPRHead("pull_request.synchronize", payload(newer)) != newer || observedPRHead("pull_request.synchronize", payload(older)) != older {
		t.Fatal("normalization must preserve observed identity, never infer chronological order")
	}
	if observedPRHead("issues.opened", payload(newer)) != "" {
		t.Fatal("non-PR delivery supplied observed head")
	}
	if observedPRHead("pull_request.opened", payload("attacker-controlled-text")) != "" {
		t.Fatal("malformed head accepted")
	}
}
