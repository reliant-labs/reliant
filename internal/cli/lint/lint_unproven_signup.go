// File: internal/cli/lint/lint_unproven_signup.go
//
// forgeconv-unproven-signup — a deployed sign-up that creates accounts whose
// address nobody proves.
//
// Where an environment's identity delivers no mail (`forge.IdentityEmail
// {delivery = "none"}`) or a broker serves the legacy self-sign-up outside
// development (auth_self_signup), a sign-up creates the account WITH its
// password and signs in at once: nothing can mail a code. Its email is then a
// contact field the person typed (devidp.MetaContactOnly). It is never
// proven, and no later proof claims the account — when mail is turned on, the
// address's real owner gets a fresh account and this one is moved aside
// (forge#2121). An app that binds anything to such an address (an
// invitation, a membership, a domain allow-list) binds it to whoever typed
// it.
//
// The lane steers to a sign-up that proves the address: deliver mail
// (`delivery = "platform"`, the default, or `smtp`), so sign-up creates no
// credential until the address is proven.
//
// Textual, like forgeconv-identity-methods: deploy/kcl/**/*.k, comments
// stripped. The dev env (deploy/kcl/dev/) is skipped for auth_self_signup,
// which the broker turns on in development anyway.
//
// ERROR severity, graded by the rollout table: a warning until GatesFrom.
package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ruleUnprovenSignup is the rule ID this lane emits.
const ruleUnprovenSignup = "forgeconv-unproven-signup"

var (
	kclNoMail         = regexp.MustCompile(`\bdelivery\s*[:=]\s*"none"`)
	kclLegacySelfSign = regexp.MustCompile(`(?i)\bauth_self_signup"?\s*[:=]\s*("?true"?|"1")`)
)

// unprovenSignupFinding is one declaration of a sign-up nobody proves.
type unprovenSignupFinding struct {
	Where string // file:line
	What  string
}

func findUnprovenSignups(root string) ([]unprovenSignupFinding, error) {
	var out []unprovenSignupFinding
	dir := filepath.Join(root, "deploy", "kcl")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".k") || strings.HasSuffix(path, "_test.k") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		lines := strings.Split(string(raw), "\n")
		for i := range lines {
			lines[i] = kclLineComment.ReplaceAllString(lines[i], "")
		}
		text := strings.Join(lines, "\n")
		lineOf := func(off int) int { return strings.Count(text[:off], "\n") + 1 }
		if signup := kclSignup.FindStringSubmatchIndex(text); signup != nil && kclNoMail.MatchString(text) {
			out = append(out, unprovenSignupFinding{
				Where: fmt.Sprintf("%s:%d", rel, lineOf(signup[0])),
				What:  fmt.Sprintf("signup %q with email delivery \"none\"", text[signup[2]:signup[3]]),
			})
		}
		if !strings.HasPrefix(rel, "deploy/kcl/dev/") {
			if m := kclLegacySelfSign.FindStringIndex(text); m != nil {
				out = append(out, unprovenSignupFinding{
					Where: fmt.Sprintf("%s:%d", rel, lineOf(m[0])),
					What:  "auth_self_signup (the legacy sign-up) outside development",
				})
			}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where < out[j].Where })
	return out, nil
}

// unprovenSignupLane is the lane's name in lintPipeline (and lint.lanes).
const unprovenSignupLane = "unproven-signup lint"

func collectUnprovenSignupJSON(root string) ([]lintJSONFinding, error) {
	if root == "" {
		return nil, nil
	}
	found, err := findUnprovenSignups(root)
	if err != nil {
		return nil, fmt.Errorf("scan identity sign-up: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(found))
	for _, f := range found {
		file, line := f.Where, 0
		if i := strings.LastIndexByte(f.Where, ':'); i > 0 {
			file = f.Where[:i]
			_, _ = fmt.Sscanf(f.Where[i+1:], "%d", &line)
		}
		out = append(out, lintJSONFinding{
			Rule:     ruleUnprovenSignup,
			Severity: "error",
			File:     file,
			Line:     line,
			Message: "this environment declares " + f.What + ": a sign-up creates its account with a password and an address " +
				"nobody proves. That address is a contact field the person typed, never proven and never claimed by a later proof",
			FixHint: "deliver mail so sign-up proves the address first: drop `delivery = \"none\"` (platform delivery is the default) " +
				"or declare `smtp`, and serve sign-up through the LOGIN_POLICY rather than auth_self_signup " +
				"(`forge skill load migrations/v1.1.0/identity-v2`)",
		})
	}
	return out, nil
}
