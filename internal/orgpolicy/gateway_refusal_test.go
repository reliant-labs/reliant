// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"net/http"
	"strings"
	"testing"
)

func TestGatewayRefusalReason(t *testing.T) {
	withReason := func(r string) http.Header {
		h := http.Header{}
		h.Set("X-Forge-Error-Reason", r)
		return h
	}
	for name, tc := range map[string]struct {
		status int
		header http.Header
		code   string
		want   string
	}{
		"a 403 naming a policy reason":            {http.StatusForbidden, withReason(ReasonModelNotAllowed), "", ReasonModelNotAllowed},
		"the body's code when the header is lost": {http.StatusForbidden, http.Header{}, ReasonOrgSpendCapReached, ReasonOrgSpendCapReached},
		"the header wins over the body":           {http.StatusForbidden, withReason(ReasonProviderNotAllowed), ReasonModelNotAllowed, ReasonProviderNotAllowed},
		"a 403 naming another reason":             {http.StatusForbidden, withReason("not_org_member"), "forbidden", ""},
		"a 429 is the wallet's, never the policy": {http.StatusTooManyRequests, withReason(ReasonMemberSpendCapReached), ReasonMemberSpendCapReached, ""},
		"no header at all":                        {http.StatusForbidden, nil, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := GatewayRefusalReason(tc.status, tc.header, tc.code); got != tc.want {
				t.Errorf("GatewayRefusalReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRefusalToolResult(t *testing.T) {
	for _, reason := range []string{ReasonModelNotAllowed, ReasonProviderNotAllowed, ReasonMemberSpendCapReached, ReasonOrgSpendCapReached, "something_new"} {
		t.Run(reason, func(t *testing.T) {
			got := (&Refusal{Reason: reason, Detail: "gpt-image-2 is not allowed."}).ToolResult("Image generation")
			for _, want := range []string{"Image generation was refused", "Your organization's policy", "do not retry", "(gpt-image-2 is not allowed)"} {
				if !strings.Contains(got, want) {
					t.Errorf("%q lacks %q", got, want)
				}
			}
			if strings.Contains(got, "resume") {
				t.Errorf("a tool result has no turn to resume: %q", got)
			}
		})
	}
}
