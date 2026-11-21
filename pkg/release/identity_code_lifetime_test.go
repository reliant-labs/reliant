package release

import (
	"strings"
	"testing"
	"time"
)

// forge#2265: a declared password-set code lifetime is a bounded Go duration,
// the owner's alone, read back with a one-hour default, and stripped for a
// control plane that predates it rather than refused there.
func TestShapeIdentity_CodeLifetime(t *testing.T) {
	base := func() ShapeIdentity {
		return ShapeIdentity{Name: "identity", Runtime: IdentityRuntimeHosted, Login: ShapeIdentityLogin{Mode: IdentityLoginNative}}
	}
	if got := base().PasswordCodeLifetime(); got != time.Hour {
		t.Errorf("default = %s, want 1h", got)
	}
	day := base()
	day.CodeLifetime = "1440m"
	if got := day.PasswordCodeLifetime(); got != 24*time.Hour {
		t.Errorf("1440m = %s", got)
	}
	for v, want := range map[string]string{
		"1440m": "", "5m": "", "168h": "",
		"4m": "outside", "169h": "outside", "soon": "not a duration",
	} {
		id := base()
		id.CodeLifetime = v
		err := id.validate(nil)
		if (err == nil) != (want == "") || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("code_lifetime %q: %v, want %q", v, err, want)
		}
	}
	sharer := base()
	sharer.ShareWith, sharer.CodeLifetime = "staging", "24h"
	if err := sharer.validate(nil); err == nil || !strings.Contains(err.Error(), "code_lifetime belongs to the owner") {
		t.Errorf("a sharer declaring a lifetime: %v", err)
	}

	s := Shape{Identity: &day}
	old := (&BundleReader{Features: []string{BundleFeatureIdentitySignIn}}).LimitShape(s)
	if old.Identity.CodeLifetime != "" || s.Identity.CodeLifetime != "1440m" {
		t.Errorf("limit for an older reader: %q (declaration now %q)", old.Identity.CodeLifetime, s.Identity.CodeLifetime)
	}
	cur := (&BundleReader{Features: []string{BundleFeatureIdentityCodeLifetime}}).LimitShape(s)
	if cur.Identity.CodeLifetime != "1440m" {
		t.Errorf("a reader that names the feature lost the lifetime: %q", cur.Identity.CodeLifetime)
	}
}
