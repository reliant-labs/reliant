package cmdutil

import (
	"strings"
	"testing"
)

// TestReservedServiceNameErrorIsActionable covers the message a user gets
// when a reserved noun is rejected.
//
// The reserved set is small and hand-written, and nothing else in the CLI
// enumerates it — `forge project capabilities` does not list it. A rejection
// that names only the offending word therefore sends the reader guessing at
// what else is disallowed, and `job` is the case that hurts: it is the
// natural noun for field service, construction, scheduling, print and
// logistics, so the author has a real domain concept and needs a name for it
// now.
func TestReservedServiceNameErrorIsActionable(t *testing.T) {
	err := ValidateServiceName("job")
	if err == nil {
		t.Fatal("`job` is reserved and must be rejected")
	}
	msg := err.Error()

	// The whole set, so the reader learns the rule rather than one instance.
	for _, name := range ReservedServiceNameList() {
		if !strings.Contains(msg, name) {
			t.Errorf("error should enumerate the reserved set (missing %q):\n%s", name, msg)
		}
	}
	// Both routes forward: the worker path, and a real alternative for a
	// domain entity that happens to share the name.
	if !strings.Contains(msg, "scaffold worker job") {
		t.Errorf("error should name the worker path:\n%s", msg)
	}
	if !strings.Contains(msg, "workorder") {
		t.Errorf("error should suggest a concrete alternative service name:\n%s", msg)
	}
}

// TestReservedServiceNameListIsStable pins ordering, since the list is
// rendered into an error message and an unstable order makes the output
// differ run to run for no reason.
func TestReservedServiceNameListIsStable(t *testing.T) {
	first := ReservedServiceNameList()
	for i := 0; i < 5; i++ {
		got := ReservedServiceNameList()
		if len(got) != len(first) {
			t.Fatalf("length changed between calls: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("order changed between calls: %v vs %v", got, first)
			}
		}
	}
	if len(first) != len(ReservedServiceNames) {
		t.Errorf("list has %d entries, map has %d — they must agree",
			len(first), len(ReservedServiceNames))
	}
}

// TestNonReservedNamesPass guards against the reserved set growing to catch
// ordinary domain nouns. Everything here is a name a real project would
// plausibly want.
func TestNonReservedNamesPass(t *testing.T) {
	for _, name := range []string{
		"workorder", "dispatch", "booking", "crew", "schedule",
		"invoice", "customer", "inventory", "shipment",
	} {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("%q is an ordinary domain noun and must be allowed: %v", name, err)
		}
	}
}
