package cli

import "testing"

func TestInstallSummary(t *testing.T) {
	for _, tc := range []struct{ name, output, want string }{
		{"pnpm", "Packages: +631\n++++\nProgress: resolved 631, reused 0, downloaded 633, added 631, done\n", "631 packages installed"},
		{"pnpm, two frontends", "Packages: +631\nPackages: +12\n", "643 packages installed"},
		{"npm", "added 412 packages, and audited 413 packages in 9s\n", "412 packages installed"},
		{"npm, one", "added 1 package in 1s\n", "1 package installed"},
		{"nothing said", "Already up to date\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installSummary(tc.output); got != tc.want {
				t.Errorf("installSummary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenerateSummary(t *testing.T) {
	out := "⏩ compile check skipped (pass --verify to run it here; your first `go build` or `forge generate` runs it anyway)\n" +
		"✅ Code generation complete — 82 file(s) written, 72 changed (63 new, 9 updated).\n"
	if got, want := generateSummary(out), "82 files written; compile check skipped (--verify runs it)"; got != want {
		t.Errorf("generateSummary = %q, want %q", got, want)
	}
	if got := generateSummary("🔨 Validating generated code...\n✅ Code generation complete — 3 file(s) written.\n"); got != "3 files written" {
		t.Errorf("generateSummary with --verify = %q", got)
	}
}
