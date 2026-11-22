package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
)

func TestResolveNewConnectMajor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		changed bool
		value   string
		kind    string
		want    connectmajor.Major
		wantErr string
	}{
		// Not passed: zero, so resolveScaffoldConnectMajor decides — v2 for
		// a fresh service project, the adopted module's major for --in-place.
		{name: "unset", want: 0},
		{name: "unset with an unknown value", value: "v9", want: 0},
		{name: "v1", changed: true, value: "v1", want: connectmajor.V1},
		{name: "v2", changed: true, value: "v2", want: connectmajor.V2},
		{name: "bare number", changed: true, value: "2", want: connectmajor.V2},
		{name: "case and space", changed: true, value: " V2 ", want: connectmajor.V2},
		{name: "explicit service kind", changed: true, value: "v2", kind: "service", want: connectmajor.V2},
		{name: "unknown value", changed: true, value: "v3", wantErr: `invalid --connect "v3"`},
		{name: "empty value", changed: true, value: "", wantErr: "invalid --connect"},
		{name: "site kind", changed: true, value: "v2", kind: "site", wantErr: "--kind site has none"},
		{name: "library kind", changed: true, value: "v1", kind: "library", wantErr: "--kind library has none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveNewConnectMajor(tc.changed, tc.value, tc.kind)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("major = %v, want %v", got, tc.want)
			}
		})
	}
}

// --in-place keeps the go.mod already there, and generate reads the major
// from it: an explicit --connect that go.mod contradicts is refused, one it
// agrees with (or no go.mod at all) is not.
func TestCheckConnectMajorAgreesWithGoMod(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	const v1 = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect v1.19.0\n"
	const v2 = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
	for _, tc := range []struct {
		name    string
		gomod   string
		want    connectmajor.Major
		wantErr string
	}{
		{name: "no flag", gomod: v2, want: 0},
		{name: "no go.mod", want: connectmajor.V2},
		{name: "agrees v2", gomod: v2, want: connectmajor.V2},
		{name: "agrees v1", gomod: v1, want: connectmajor.V1},
		{name: "v2 asked, v1 go.mod", gomod: v1, want: connectmajor.V2, wantErr: "--connect v2, but the go.mod here selects connect-go v1"},
		{name: "v1 asked, v2 go.mod", gomod: v2, want: connectmajor.V1, wantErr: "--connect v1, but the go.mod here selects connect-go v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkConnectMajorAgreesWithGoMod(write(t, tc.gomod), tc.want)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// The flag is on `project new`, and its help says where the major comes from
// once the project exists.
func TestNewCmdHasConnectFlag(t *testing.T) {
	t.Parallel()
	cmd := newNewCmd()
	f := cmd.Flags().Lookup("connect")
	if f == nil {
		t.Fatal("project new has no --connect flag")
	}
	if f.DefValue != "" {
		t.Fatalf("--connect default = %q, want unset (v2 for a new service project, the go.mod's major in place)", f.DefValue)
	}
	for _, w := range []string{"v1", "v2", "go.mod"} {
		if !strings.Contains(f.Usage, w) {
			t.Errorf("--connect usage does not mention %q: %s", w, f.Usage)
		}
	}
	if !strings.Contains(cmd.Long, "--connect") {
		t.Error("project new's help does not explain --connect")
	}
}

// The parsed choice reaches the generator that renders go.mod.
func TestConfigureProjectGenerator_SetsConnectMajor(t *testing.T) {
	newConnectMajor = connectmajor.V2
	t.Cleanup(func() { newConnectMajor = 0 })
	gen, err := configureProjectGenerator(newGeneratorConfig{projectName: "app", targetPath: t.TempDir(), modulePath: "example.com/app", kind: "service", harness: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if gen.ConnectMajor != connectmajor.V2 {
		t.Fatalf("ProjectGenerator.ConnectMajor = %v, want v2", gen.ConnectMajor)
	}
}
