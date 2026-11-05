package openbao

import (
	"encoding/json"
	"strings"
	"testing"
)

func retiredSpec() Spec {
	s := accessSpec()
	s.RetiredAuthMounts = []RetiredAuthMount{
		{Name: "kubernetes", Reason: "no login since 2026-10-25 11:41Z"},
		{Name: "approle", Reason: "no login since\n  2026-10-25 12:27Z"},
	}
	return s
}

func TestRetired_ValidateRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mounts []RetiredAuthMount
		want   string
	}{
		"no reason":      {[]RetiredAuthMount{{Name: "approle"}}, "states no reason"},
		"blank reason":   {[]RetiredAuthMount{{Name: "approle", Reason: " \n "}}, "states no reason"},
		"glob":           {[]RetiredAuthMount{{Name: "jwt-*", Reason: "x"}}, "not an auth mount name"},
		"path":           {[]RetiredAuthMount{{Name: "a/b", Reason: "x"}}, "not an auth mount name"},
		"empty name":     {[]RetiredAuthMount{{Reason: "x"}}, "not an auth mount name"},
		"upper case":     {[]RetiredAuthMount{{Name: "AppRole", Reason: "x"}}, "not an auth mount name"},
		"token auth":     {[]RetiredAuthMount{{Name: "token", Reason: "x"}}, "built-in token auth"},
		"declared twice": {[]RetiredAuthMount{{Name: "approle", Reason: "x"}, {Name: "approle", Reason: "y"}}, "declared twice"},
	} {
		t.Run(name, func(t *testing.T) {
			s := raftSpec()
			s.RetiredAuthMounts = tc.mounts
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
	s := raftSpec()
	s.RetiredAuthMounts = []RetiredAuthMount{{Name: "approle", Reason: "unused"}, {Name: "kubernetes", Reason: "unused", Force: true}}
	if err := s.Validate(); err != nil {
		t.Fatalf("a well-formed retire list must validate: %v", err)
	}
}

// Retiring a mount the document itself writes would enable then disable it on
// every run, and retiring the self mount would remove the converge Job's own
// login. Both are refused when the document is derived, not left to the script.
func TestRetired_RefusesAManagedMount(t *testing.T) {
	for name, mount := range map[string]string{
		"a managed cluster mount": "jwt-daemon",
		"the self mount":          "jwt-local",
	} {
		t.Run(name, func(t *testing.T) {
			s := accessSpec()
			s.RetiredAuthMounts = []RetiredAuthMount{{Name: mount, Reason: "x"}}
			_, err := BuildAccess(s)
			if err == nil || !strings.Contains(err.Error(), mount) {
				t.Fatalf("retiring %s must be refused naming it, got %v", mount, err)
			}
		})
	}
	// A jwt-* mount the component does NOT manage is a legacy one and can go.
	s := accessSpec()
	s.RetiredAuthMounts = []RetiredAuthMount{{Name: "jwt-old-cluster", Reason: "cluster decommissioned"}}
	if _, err := BuildAccess(s); err != nil {
		t.Fatalf("a jwt-* mount the component does not manage may be retired: %v", err)
	}
}

// The policy gets exactly the names listed, never a glob, and only the
// converge Job holds it: the access-admin policy that delegated principals
// carry must not be able to disable a mount.
func TestRetired_PolicyGrantsExactlyTheListedNames(t *testing.T) {
	doc, err := BuildAccess(retiredSpec())
	if err != nil {
		t.Fatal(err)
	}
	conv := findPolicy(t, doc, PolicyConverge)
	got := map[string][]string{}
	for _, r := range conv.Rules {
		got[r.Path] = r.Capabilities
	}
	for _, name := range []string{"kubernetes", "approle"} {
		if c := strings.Join(got["sys/auth/"+name], ","); c != "delete,sudo" {
			t.Errorf("sys/auth/%s capabilities = %q, want delete,sudo", name, c)
		}
		if c := strings.Join(got["auth/"+name+"/role"], ","); c != "list" {
			t.Errorf("auth/%s/role capabilities = %q, want list (the in-use check)", name, c)
		}
	}
	// Every path under sys/auth/ or auth/ the policy names is either the
	// pre-existing jwt-* grant or exactly one of the retired names.
	for _, r := range conv.Rules {
		for _, prefix := range []string{"sys/auth/", "auth/"} {
			rest, ok := strings.CutPrefix(r.Path, prefix)
			if !ok || strings.HasPrefix(rest, "jwt-") {
				continue
			}
			base, _, _ := strings.Cut(rest, "/")
			if base != "kubernetes" && base != "approle" {
				t.Errorf("rule %q grants on a mount nobody retired", r.Path)
			}
			if strings.ContainsAny(r.Path, "*+") {
				t.Errorf("rule %q is a glob: retiring grants exact names only", r.Path)
			}
		}
	}
	for _, r := range findPolicy(t, doc, PolicyAccessAdmin).Rules {
		if strings.Contains(r.Path, "kubernetes") || strings.Contains(r.Path, "approle") {
			t.Errorf("access-admin (held by delegated principals) must not gain %q", r.Path)
		}
	}
	if len(doc.Retired) != 2 || doc.Retired[0].Name != "approle" || doc.Retired[1].Name != "kubernetes" {
		t.Fatalf("retired = %+v, want approle, kubernetes (sorted)", doc.Retired)
	}
	if strings.ContainsAny(doc.Retired[0].Reason, "\n") || doc.Retired[0].Reason != "no login since 2026-10-25 12:27Z" {
		t.Errorf("the reason is normalised onto one line, got %q", doc.Retired[0].Reason)
	}
}

func TestRetired_NothingRetiredAddsNothing(t *testing.T) {
	base, err := BuildAccess(accessSpec())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base.JSON()
	if strings.Contains(string(raw), "retired") {
		t.Fatalf("an empty retire list must not appear in the document (the Job is content-addressed):\n%s", raw)
	}
	conv := findPolicy(t, base, PolicyConverge)
	for _, r := range conv.Rules {
		if strings.HasPrefix(r.Path, "sys/auth/") && r.Path != "sys/auth/jwt-*" {
			t.Errorf("converge policy gained %q with nothing retired", r.Path)
		}
	}
}

func TestRetired_DeterministicAndContentAddressed(t *testing.T) {
	a, _ := BuildAccess(retiredSpec())
	s := retiredSpec()
	s.RetiredAuthMounts[0], s.RetiredAuthMounts[1] = s.RetiredAuthMounts[1], s.RetiredAuthMounts[0]
	b, _ := BuildAccess(s)
	if a.Hash() != b.Hash() {
		t.Fatal("the same retire list in another order must give the same document")
	}
	base, _ := BuildAccess(accessSpec())
	if a.Hash() == base.Hash() {
		t.Fatal("retiring a mount must change the document, so the converge Job re-runs")
	}
	s = retiredSpec()
	s.RetiredAuthMounts[0].Force = true
	c, _ := BuildAccess(s)
	if a.Hash() == c.Hash() {
		t.Fatal("force must change the document")
	}
}

// Widen is the pre-rollout union: it must never carry a retire, which is a
// converge-phase act after the rollout.
func TestRetired_WidenNeverRetires(t *testing.T) {
	old, _ := BuildAccess(accessSpec())
	next, _ := BuildAccess(retiredSpec())
	w := Widen(old, next)
	if len(w.Retired) != 0 {
		t.Fatalf("widen carries retire entries: %+v", w.Retired)
	}
	if gone := Removed(old, next); len(gone.Mounts) != 0 {
		t.Fatalf("declaring a retire must not make narrowing remove a managed mount: %+v", gone.Mounts)
	}
}

// The record reaches the Job as the ConfigMap's access.json.
func TestRetired_RenderedIntoTheJobsDocument(t *testing.T) {
	s := raftSpec()
	s.RetiredAuthMounts = []RetiredAuthMount{{Name: "approle", Reason: "unused", Force: true}}
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		data, _ := o.Object["data"].(map[string]any)
		raw, ok := data["access.json"].(string)
		if !ok {
			continue
		}
		var doc AccessDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Retired) != 1 || doc.Retired[0] != (RetiredAuthMount{Name: "approle", Reason: "unused", Force: true}) {
			t.Fatalf("the Job's document retires %+v", doc.Retired)
		}
		return
	}
	t.Fatal("no access.json rendered")
}
