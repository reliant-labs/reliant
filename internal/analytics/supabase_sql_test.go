// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"os"
	"testing"

	"github.com/reliant-labs/forge/pkg/authhooks"
)

// deploy/supabase/forge_auth_events.sql is what an operator runs on the
// Supabase project (dev-docs/supabase-auth-events.md). It must be forge's
// file verbatim, the one the pinned forge's /_forge/auth/hooks/supabase
// handler reads: regenerate it with `reliant forge auth hooks sql supabase >
// deploy/supabase/forge_auth_events.sql` after a forge bump changes it, and
// re-apply it to each project.
func TestSupabaseAuthEventsSQLIsForgesVerbatim(t *testing.T) {
	got, err := os.ReadFile("../../deploy/supabase/forge_auth_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != authhooks.SupabaseSQL() {
		t.Fatal("deploy/supabase/forge_auth_events.sql differs from the pinned forge's authhooks.SupabaseSQL(): " +
			"run `reliant forge auth hooks sql supabase > deploy/supabase/forge_auth_events.sql`, then re-apply it")
	}
}
