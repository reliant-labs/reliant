package deploytarget

import (
	"os"
	"path/filepath"
	"testing"
)

// A scaffolded frontend pins pnpm in package.json and declares no dev runner.
// The static build must install it with pnpm: `npm install` over the pnpm
// node_modules tree `forge project new` wrote crashes npm's arborist, which
// failed every first `forge env deploy prod` of a scaffolded project.
func TestFrontendInstallCmdFollowsTheOwningPackageManager(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		declared string
		want     string
	}{
		{
			name:  "packageManager pin, no runner declared",
			files: map[string]string{"package.json": `{"packageManager": "pnpm@12.15.0"}`, "pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
			want:  "pnpm",
		},
		{
			name:     "pnpm lockfile beats an npm declaration",
			files:    map[string]string{"package.json": `{}`, "pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
			declared: "npm",
			want:     "pnpm",
		},
		{
			name:  "plain npm project",
			files: map[string]string{"package.json": `{}`, "package-lock.json": "{}"},
			want:  "npm",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			argv := frontendInstallCmd(dir, tc.declared)
			if !containsArg(argv, tc.want) || !containsArg(argv, "install") {
				t.Fatalf("frontendInstallCmd = %q, want a %s install", argv, tc.want)
			}
			if tc.want == "pnpm" && argv[0] == "npm" && !containsArg(argv, "exec") {
				t.Fatalf("frontendInstallCmd = %q runs npm itself for a pnpm frontend", argv)
			}
		})
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want || filepath.Base(a) == want || len(a) > len(want) && a[:len(want)+1] == want+"@" {
			return true
		}
	}
	return false
}
