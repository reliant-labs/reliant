// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
)

// Every reason reads as a sentence that says what was refused and what to do,
// carries the marker with the reason as payload, and is a user error; the
// refusal is recovered both typed and from text alone.
func TestRefusal(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{ReasonModelNotAllowed, ReasonProviderNotAllowed, ReasonMemberSpendCapReached, ReasonOrgSpendCapReached} {
		if !IsRefusalReason(reason) {
			t.Fatalf("%s is not a refusal reason", reason)
		}
		r := &Refusal{Reason: reason, Detail: "set by an org admin."}
		msg := r.Error()
		if !strings.HasPrefix(msg, chatmarkers.OrgPolicyRefusedLead) || !strings.Contains(msg, "(set by an org admin)") || !strings.Contains(msg, "then resume") {
			t.Errorf("%s: message %q", reason, msg)
		}
		if kind, payload, ok := chatmarkers.Extract(msg); !ok || kind != chatmarkers.KindOrgPolicyRefused || payload != reason {
			t.Errorf("%s: marker = %v %q %v", reason, kind, payload, ok)
		}
		if chatmarkers.OrgPolicyRefusedSummary(msg) == "" {
			t.Errorf("%s: no summary sentence", reason)
		}
		if svcerr.Classify(r) != svcerr.ClassUser {
			t.Errorf("%s: class %v", reason, svcerr.Classify(r))
		}
		typed, ok := RefusalOf(fmt.Errorf("call llm: %w", r))
		if !ok || typed != r {
			t.Errorf("%s: typed refusal lost through a wrap", reason)
		}
		text, ok := RefusalOf(errors.New("activity error: " + msg))
		if !ok || text.Reason != reason {
			t.Errorf("%s: refusal not recovered from text: %+v", reason, text)
		}
	}
	for _, not := range []string{"", "insufficient_quota", "forbidden", "not_org_member"} {
		if IsRefusalReason(not) {
			t.Errorf("%q read as a refusal reason", not)
		}
	}
	if IsRefusal(nil) || IsRefusal(errors.New("403 Forbidden")) {
		t.Error("a plain error read as a refusal")
	}
}
