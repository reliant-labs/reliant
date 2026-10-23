package config

import (
	"testing"

	"google.golang.org/protobuf/types/known/durationpb"
)

// DefaultsInto is the config a test hands a component: what LoadInto yields
// with no file, environment or flag. A nested block must come back
// allocated and carrying its default — an empty message leaves it nil, and
// every defaulted leaf behind a nil-safe getter then reads as zero (#749).
func TestDefaultsInto_NestedBlockIsAllocatedWithItsDefaults(t *testing.T) {
	m := buildBlockMessage(t)
	if err := DefaultsInto(m.Interface()); err != nil {
		t.Fatalf("DefaultsInto: %v", err)
	}
	if !m.Has(m.Descriptor().Fields().ByName("trader")) {
		t.Fatal("trader block is unset; DefaultsInto must allocate nested config blocks")
	}
	if got := blockIntOf(t, m, "trader", "max_per_tick"); got != 10 {
		t.Errorf("trader.max_per_tick = %d, want its default 10", got)
	}
	if got := intOf(m, "port"); got != 8080 {
		t.Errorf("port = %d, want its default 8080", got)
	}
}

// The defaults layer reads nothing from the process: an environment that
// would override a leaf under LoadInto leaves DefaultsInto's answer alone, so
// a test's config does not depend on the shell that runs it.
func TestDefaultsInto_IgnoresTheEnvironment(t *testing.T) {
	t.Setenv("TRADER_MAX_PER_TICK", "50")
	t.Setenv("PORT", "9999")
	m := buildBlockMessage(t)
	if err := DefaultsInto(m.Interface()); err != nil {
		t.Fatalf("DefaultsInto: %v", err)
	}
	if got := blockIntOf(t, m, "trader", "max_per_tick"); got != 10 {
		t.Errorf("trader.max_per_tick = %d, want the default 10 (env must not apply)", got)
	}
	if got := intOf(m, "port"); got != 8080 {
		t.Errorf("port = %d, want the default 8080 (env must not apply)", got)
	}
}

// Sensitive fields get no default (an inline secret default would be a
// leak), and a required field left empty is not an error here: the
// required check belongs to Load, where some layer must satisfy it.
func TestDefaultsInto_SensitiveStaysEmptyAndRequiredIsNotChecked(t *testing.T) {
	m := buildTestMessage(t)
	if err := DefaultsInto(m.Interface()); err != nil {
		t.Fatalf("DefaultsInto: %v (a required field with no default must not fail the defaults layer)", err)
	}
	if got := strOf(m, "api_key"); got != "" {
		t.Errorf("api_key (sensitive) = %q, want empty", got)
	}
	if got := strOf(m, "log_level"); got != "info" {
		t.Errorf("log_level = %q, want its default info", got)
	}
	if d := durOf(t, m, "timeout"); d.AsDuration().String() != "30s" {
		t.Errorf("timeout = %v, want its default 30s", d.AsDuration())
	}
	if got := strOf(m, "ignored"); got != "" {
		t.Errorf("unannotated field = %q, want empty", got)
	}
}

// DefaultsTyped allocates a fresh T from the type alone (the generated
// config.Defaults() calls it with no value in hand).
func TestDefaultsTyped_AllocatesAFreshMessage(t *testing.T) {
	a, err := DefaultsTyped[*durationpb.Duration]()
	if err != nil {
		t.Fatalf("DefaultsTyped: %v", err)
	}
	b, err := DefaultsTyped[*durationpb.Duration]()
	if err != nil {
		t.Fatalf("DefaultsTyped: %v", err)
	}
	if a == nil || b == nil {
		t.Fatal("DefaultsTyped returned nil")
	}
	if a == b {
		t.Error("DefaultsTyped returned the same instance twice; each call must allocate")
	}
}
