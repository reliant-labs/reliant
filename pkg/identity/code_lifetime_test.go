package identity

import (
	"context"
	"strings"
	"testing"
	"time"
)

// forge#2265: a declared code lifetime converges the instance's
// password-reset code generator — expiry only, length and alphabet kept —
// once, and a minted code's ExpiresAt follows it. Zero declares nothing.
func TestReconcile_CodeLifetime(t *testing.T) {
	const gen = "SECRET_GENERATOR_TYPE_PASSWORD_RESET_CODE"
	f := newFake(t)
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	f.mu.Lock()
	f.now = func() time.Time { return at }
	f.generators[gen] = map[string]any{"length": float64(8), "expiry": "3600s", "includeUpperLetters": true, "includeDigits": true}
	f.mu.Unlock()
	r, _ := newRec(t, f, func(o *Options) { o.SignInCodes = true })
	ctx := context.Background()
	spec := Spec{Project: ProjectSpec{Name: "p"}, Roles: []Role{{Key: "member", Declared: true, Members: []string{"new@example.com"}}}}

	if _, err := r.Reconcile(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if got := f.generators[gen]["expiry"]; got != "3600s" {
		t.Fatalf("no lifetime declared, yet the generator was written: %v", f.generators[gen])
	}

	spec.CodeLifetime = 24 * time.Hour
	rep, err := r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	g := f.generators[gen]
	if g["expiry"] != "86400s" || g["length"] != float64(8) || g["includeUpperLetters"] != true || g["includeDigits"] != true || g["includeLowerLetters"] != false {
		t.Fatalf("generator = %v, want expiry 86400s with the length and alphabet kept", g)
	}
	if len(rep.SignInCodes) != 1 {
		t.Fatalf("sign-in codes = %+v, want one", rep.SignInCodes)
	}
	for _, c := range rep.SignInCodes {
		if !c.ExpiresAt.Equal(at.Add(24 * time.Hour)) {
			t.Errorf("ExpiresAt %v ignores the declared lifetime (want %v)", c.ExpiresAt, at.Add(24*time.Hour))
		}
	}
	again, err := r.Reconcile(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range again.Changes {
		if ch.Kind == "secret generator" {
			t.Errorf("a converged generator was written again: %+v", ch)
		}
	}

	for _, d := range []time.Duration{4 * time.Minute, 8 * 24 * time.Hour} {
		if err := (Spec{Project: ProjectSpec{Name: "p"}, CodeLifetime: d}).Validate(); err == nil {
			t.Errorf("CodeLifetime %s validated", d)
		}
	}
}

// forge#2338 [B1]: a password-set code is minted at the generator's expiry,
// whatever ExpiresAt says. While the generator cannot be read or converged
// to the declared lifetime, no code is minted (no password_reset call, no
// SignInCode), and every other step of the pass still runs. The next pass
// that converges the generator issues it.
func TestReconcile_CodeLifetimeWithheldWhileGeneratorFails(t *testing.T) {
	for _, reject := range []string{
		"GET /admin/v1/secretgenerators/SECRET_GENERATOR_TYPE_PASSWORD_RESET_CODE",
		"PUT /admin/v1/secretgenerators/SECRET_GENERATOR_TYPE_PASSWORD_RESET_CODE",
	} {
		t.Run(reject[:3], func(t *testing.T) {
			f := newFake(t)
			at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			f.mu.Lock()
			f.now = func() time.Time { return at }
			f.generators["SECRET_GENERATOR_TYPE_PASSWORD_RESET_CODE"] = map[string]any{"length": float64(6), "expiry": "604800s", "includeUpperLetters": true, "includeDigits": true}
			f.failPath[reject] = 403
			f.mu.Unlock()
			r, _ := newRec(t, f, func(o *Options) { o.SignInCodes = true })
			ctx := context.Background()
			spec := Spec{Project: ProjectSpec{Name: "p"}, CodeLifetime: 5 * time.Minute,
				Roles: []Role{{Key: "member", Declared: true, Members: []string{"new@example.com"}}}}

			rep, err := r.Reconcile(ctx, spec)
			if err == nil {
				t.Fatal("the generator was rejected, yet the pass reported success")
			}
			if rep == nil || len(rep.SignInCodes) != 0 {
				t.Fatalf("sign-in codes = %+v while the generator is at an unknown expiry; want none", rep)
			}
			f.mu.Lock()
			for _, l := range f.log {
				if strings.HasSuffix(l, "/password_reset") {
					t.Errorf("a password reset was requested while the generator is unconverged: %s", l)
				}
			}
			f.mu.Unlock()
			if f.user("new@example.com") == nil {
				t.Error("the member was not created: the generator's failure stopped an independent step")
			}

			f.mu.Lock()
			delete(f.failPath, reject)
			f.mu.Unlock()
			rep, err = r.Reconcile(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.SignInCodes) != 1 || !rep.SignInCodes[0].ExpiresAt.Equal(at.Add(5*time.Minute)) {
				t.Fatalf("sign-in codes = %+v once the generator converged; want one expiring %v", rep.SignInCodes, at.Add(5*time.Minute))
			}
		})
	}
}
