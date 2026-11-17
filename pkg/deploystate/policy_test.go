package deploystate

import (
	"encoding/json"
	"testing"
)

// TestPolicyZeroValueIsObserve is the single most important assertion in
// this package, and it is written against the ZERO VALUE rather than
// against ParsePolicy("") on purpose: the guarantee is about every path
// that can produce a Policy, including a struct literal that never went
// through a decoder.
func TestPolicyZeroValueIsObserve(t *testing.T) {
	var p Policy
	if p != PolicyObserve {
		t.Fatalf("zero Policy = %v, want PolicyObserve", p)
	}
	if p.AllowsConverge() {
		t.Fatal("zero Policy allows converge; a forgotten field must never permit a write")
	}
	if !p.AllowsChange() {
		t.Fatal("zero Policy refuses ordinary changes; only pinned should")
	}

	// The same guarantee through the struct that carries it on disk.
	var pf policyFile
	if pf.Policy != PolicyObserve {
		t.Fatalf("zero policyFile.Policy = %v, want PolicyObserve", pf.Policy)
	}

	// And through a JSON document that omits the field entirely — the
	// shape a policy file written by a forge predating this field has.
	var decoded policyFile
	if err := json.Unmarshal([]byte(`{}`), &decoded); err != nil {
		t.Fatalf("unmarshal empty object: %v", err)
	}
	if decoded.Policy != PolicyObserve {
		t.Fatalf("policy absent from JSON decoded to %v, want PolicyObserve", decoded.Policy)
	}
}

func TestParsePolicy(t *testing.T) {
	cases := []struct {
		in      string
		want    Policy
		wantErr bool
	}{
		{"", PolicyObserve, false},
		{"observe", PolicyObserve, false},
		{"converge", PolicyConverge, false},
		{"pinned", PolicyPinned, false},
		// A typo must NOT silently become the default: a user who typed
		// this meant to change behaviour.
		{"converg", PolicyObserve, true},
		{"CONVERGE", PolicyObserve, true},
		{"true", PolicyObserve, true},
	}
	for _, tc := range cases {
		got, err := ParsePolicy(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("ParsePolicy(%q) = %v, nil; want an error", tc.in, got)
			continue
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ParsePolicy(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePolicy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestPolicyUnknownValueNeverDecodesToConverge pins the direction of the
// failure. A malformed policy may land on observe; it must never land on
// converge, because that would mean a corrupt file grants forge write
// access it was never given.
func TestPolicyUnknownValueNeverDecodesToConverge(t *testing.T) {
	for _, raw := range []string{`{"policy":"garbage"}`, `{"policy":"CONVERGE"}`, `{"policy":"conv"}`} {
		var pf policyFile
		err := json.Unmarshal([]byte(raw), &pf)
		if err == nil {
			t.Errorf("%s: decoded without error, want a rejection", raw)
		}
		if pf.Policy == PolicyConverge {
			t.Errorf("%s: decoded to PolicyConverge; a bad value must never grant write access", raw)
		}
	}
}

func TestPolicyJSONRoundTrip(t *testing.T) {
	for _, p := range []Policy{PolicyObserve, PolicyConverge, PolicyPinned} {
		data, err := json.Marshal(policyFile{Policy: p})
		if err != nil {
			t.Fatalf("marshal %v: %v", p, err)
		}
		var back policyFile
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		if back.Policy != p {
			t.Errorf("round trip %v -> %s -> %v", p, data, back.Policy)
		}
	}
}

// TestPolicyPinnedRefusesOrdinaryChanges is why there are three values
// rather than a bool. Observe and pinned agree about reconciliation and
// DISAGREE about a human-initiated deploy; if they agreed about both,
// one of them would be redundant.
func TestPolicyPinnedRefusesOrdinaryChanges(t *testing.T) {
	if PolicyObserve.AllowsConverge() || PolicyPinned.AllowsConverge() {
		t.Fatal("only converge may permit convergence")
	}
	if !PolicyConverge.AllowsConverge() {
		t.Fatal("converge must permit convergence")
	}

	if !PolicyObserve.AllowsChange() {
		t.Fatal("observe must still permit an ordinary human-initiated deploy")
	}
	if PolicyPinned.AllowsChange() {
		t.Fatal("pinned must refuse ALL changes, including ordinary deploys")
	}
	if !PolicyConverge.AllowsChange() {
		t.Fatal("converge must permit ordinary deploys")
	}
}
