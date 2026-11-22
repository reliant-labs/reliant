package openbao

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A transit mount reaches the document, and the converge Job gets exactly
// the grant to enable it: sys/mounts/<mount>, and nothing under the mount
// (it never creates, reads or uses a key).
func TestTransit_ConvergeGrantsEnablingOnly(t *testing.T) {
	s := accessSpec()
	s.Transit = []TransitMount{{Mount: "transit"}, {Mount: "delivery"}}
	doc, err := BuildAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Transit; len(got) != 2 || got[0].Mount != "delivery" || got[1].Mount != "transit" {
		t.Fatalf("transit = %+v, want both mounts in sorted order", got)
	}
	var converge Policy
	for _, p := range doc.Policies {
		if p.Name == PolicyConverge {
			converge = p
		}
	}
	for _, m := range []string{"transit", "delivery"} {
		if !slices.ContainsFunc(converge.Rules, func(r Rule) bool { return r.Path == "sys/mounts/"+m }) {
			t.Errorf("converge-admin cannot enable %s", m)
		}
	}
	for _, r := range converge.Rules {
		if strings.HasPrefix(r.Path, "transit/") || strings.HasPrefix(r.Path, "delivery/") {
			t.Errorf("converge-admin holds %s under a transit mount: it must not touch keys", r.Path)
		}
	}
}

// A store without transit keeps its document bytes: no "transit" key at all.
func TestTransit_AbsentKeepsTheDocument(t *testing.T) {
	doc, err := BuildAccess(accessSpec())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"transit"`) {
		t.Fatalf("a store with no transit mount renders one: %s", b)
	}
}

func TestTransit_ValidateRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mounts []TransitMount
		want   string
	}{
		"a path":       {[]TransitMount{{Mount: "a/b"}}, "single path segment"},
		"empty":        {[]TransitMount{{Mount: ""}}, "single path segment"},
		"the kv mount": {[]TransitMount{{Mount: "secret"}}, "is the KV mount"},
		"twice":        {[]TransitMount{{Mount: "transit"}, {Mount: "transit"}}, "declared twice"},
	} {
		t.Run(name, func(t *testing.T) {
			s := raftSpec()
			s.Transit = tc.mounts
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
	s := raftSpec()
	s.Transit = []TransitMount{{Mount: "transit"}}
	if err := s.Validate(); err != nil {
		t.Fatalf("one transit mount must validate: %v", err)
	}
}
