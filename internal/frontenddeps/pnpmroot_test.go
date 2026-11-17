package frontenddeps

import (
	"path/filepath"
	"testing"
)

func TestPnpmInstallRootOnlyUsesAnOwnedBridge(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bridged    bool
	}{
		{"owned", PnpmBridgeMarker + "\npackages:\n - frontends/web\n", true},
		{"user workspace", "packages:\n - frontends/web\n", false},
		{"other frontend", PnpmBridgeMarker + "\npackages:\n - frontends/other\n", false},
		{"malformed", PnpmBridgeMarker + "\npackages: [", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fe := filepath.Join(root, "frontends", "web")
			writeFileAt(t, root, "pnpm-workspace.yaml", tc.body)
			writeFileAt(t, fe, "package.json", `{"packageManager":"pnpm@12.15.0"}`)
			writeFileAt(t, fe, "pnpm-workspace.yaml", "allowBuilds: {}\n")
			want := fe
			if tc.bridged {
				want = root
			}
			if got := PnpmInstallRoot(fe); got != want {
				t.Errorf("root=%q, want %q", got, want)
			}
			if tc.bridged {
				writeFileAt(t, fe, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
				_, args, dir := lockRefreshArgs(Lock{Dir: fe, Runner: "pnpm"}, fe)
				if dir != fe || args[len(args)-1] != "--ignore-workspace" {
					t.Fatalf("tracked lock refresh escaped to bridge: dir=%s args=%v", dir, args)
				}
			}
		})
	}
}

func TestPackageManagerOrScaffold(t *testing.T) {
	dir := t.TempDir()
	if got := PackageManagerOrScaffold(dir); got != "pnpm" {
		t.Fatalf("new scaffold manager=%s", got)
	}
	writeFileAt(t, dir, "package.json", `{}`)
	if got := PackageManagerOrScaffold(dir); got != "npm" {
		t.Fatalf("existing npm manager=%s", got)
	}
}
