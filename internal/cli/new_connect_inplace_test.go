package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The scaffold's major is decided against the REAL target: an explicit
// --connect the go.mod there contradicts is refused (with or without
// --force), an unset one inherits the go.mod's major, and a directory with
// no go.mod is a new project: v2 for a service, zero (no Connect code) for
// any other kind.
func TestResolveScaffoldConnectMajor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		gomod   string
		flag    connectmajor.Major
		kind    string // default service
		want    connectmajor.Major
		wantErr string
	}{
		{name: "no go.mod, unset", want: connectmajor.V2},
		{name: "no go.mod, unset, cli", kind: "cli", want: 0},
		{name: "no go.mod, unset, library", kind: "library", want: 0},
		{name: "no go.mod, v1", flag: connectmajor.V1, want: connectmajor.V1},
		{name: "no go.mod, v2", flag: connectmajor.V2, want: connectmajor.V2},
		{name: "v1 go.mod, unset", gomod: v1ProjectGoMod, want: connectmajor.V1},
		{name: "v1 go.mod, v1", gomod: v1ProjectGoMod, flag: connectmajor.V1, want: connectmajor.V1},
		{name: "v1 go.mod, v2", gomod: v1ProjectGoMod, flag: connectmajor.V2, wantErr: "--connect v2, but the go.mod here selects connect-go v1"},
		{name: "v2 go.mod, unset", gomod: v2ProjectGoMod, want: connectmajor.V2},
		{name: "v2 go.mod, v2", gomod: v2ProjectGoMod, flag: connectmajor.V2, want: connectmajor.V2},
		{name: "v2 go.mod, v1", gomod: v2ProjectGoMod, flag: connectmajor.V1, wantErr: "--connect v1, but the go.mod here selects connect-go v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.gomod != "" {
				writeGoModIn(t, dir, tc.gomod)
			}
			kind := tc.kind
			if kind == "" {
				kind = "service"
			}
			got, err := resolveScaffoldConnectMajor(dir, tc.flag, kind)
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

// `project new --in-place` renders into an empty staging directory and
// merges into the real tree. The major used to be resolved against that
// staging directory, so an adopted v2 module was born with v1 service and
// middleware files (which generate never rewrites) and a contradicting
// --connect slipped through. Through writeScaffoldFiles itself, for every
// go.mod × --connect × --force combination: the files come out in the real
// module's major, a conflict is refused before anything is written, and
// --buf-plugins=remote names the same major's plugin.
func TestWriteScaffoldFiles_InPlaceKeepsTheGoModConnectMajor(t *testing.T) {
	testkit.Slow(t, "scaffolds whole projects, execing go and git")
	for _, gm := range []struct {
		name  string
		gomod string
		major connectmajor.Major
	}{
		{"v1 go.mod", v1ProjectGoMod, connectmajor.V1},
		{"v2 go.mod", v2ProjectGoMod, connectmajor.V2},
	} {
		other := connectmajor.V2
		if gm.major.IsV2() {
			other = connectmajor.V1
		}
		for _, fl := range []struct {
			name string
			flag connectmajor.Major
		}{{"--connect unset", 0}, {"--connect agrees", gm.major}, {"--connect conflicts", other}} {
			for _, force := range []bool{false, true} {
				name := gm.name + ", " + fl.name
				if force {
					name += ", --force"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					writeGoModIn(t, dir, gm.gomod)
					newConnectMajor = fl.flag
					t.Cleanup(func() { newConnectMajor = 0 })
					var err error
					captureStdout(t, func() {
						_, err = writeScaffoldFiles(scaffoldFilesInput{
							gen: newGeneratorConfig{
								projectName: "app", targetPath: dir, modulePath: "example.com/app",
								kind: "service", harness: "none", serviceNames: []string{"orders", "billing"},
							},
							bufPlugins: "remote",
							inPlace:    true,
							force:      force,
						})
					})
					if fl.flag == other {
						if err == nil || !strings.Contains(err.Error(), "but the go.mod here selects connect-go "+gm.major.String()) {
							t.Fatalf("err = %v, want the --connect %s conflict refused", err, other)
						}
						if _, statErr := os.Stat(filepath.Join(dir, "forge.yaml")); statErr == nil {
							t.Fatal("a refused scaffold still wrote forge.yaml")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					read := func(rel string) string {
						t.Helper()
						b, err := os.ReadFile(filepath.Join(dir, rel))
						if err != nil {
							t.Fatal(err)
						}
						return string(b)
					}
					v2 := gm.major.IsV2()
					for _, svc := range []string{"orders", "billing"} {
						src := read("internal/handlers/" + svc + "/service.go")
						if got := strings.Contains(src, "RegisterV2("); got != v2 {
							t.Errorf("%s/service.go: RegisterV2 = %v in a %s module:\n%s", svc, got, gm.major, src)
						}
					}
					if got := strings.Contains(read("pkg/middleware/middleware.go"), "NewAuthInterceptorV2"); got != v2 {
						t.Errorf("pkg/middleware/middleware.go: NewAuthInterceptorV2 = %v in a %s module", got, gm.major)
					}
					// Kept, or (--force) replaced by forge's — on the same major.
					gomod := read("go.mod")
					if !force && gomod != gm.gomod {
						t.Errorf("go.mod changed without --force:\n%s", gomod)
					}
					if got, _ := connectmajor.FromGoMod("go.mod", []byte(gomod)); got != gm.major {
						t.Errorf("go.mod now selects %s, want %s:\n%s", got, gm.major, gomod)
					}
					if bufGen := read("buf.gen.yaml"); !strings.Contains(bufGen, "  - remote: "+remoteConnectGoPlugin(gm.major)+"\n") {
						t.Errorf("buf.gen.yaml does not name %s:\n%s", remoteConnectGoPlugin(gm.major), bufGen)
					}
				})
			}
		}
	}
}

// --buf-plugins=remote: a v1 project's buf.gen.yaml is the unversioned
// plugin it always was; a v2 project's pins connect-go v2's plugin at the
// release the scaffolded go.mod requires.
func TestRewriteBufGenYamlToRemote_ConnectMajor(t *testing.T) {
	t.Parallel()
	render := func(t *testing.T, major connectmajor.Major) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "buf.gen.yaml"), []byte("version: v2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := rewriteBufGenYamlToRemote(dir, major); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "buf.gen.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	v1 := render(t, connectmajor.V1)
	if unset := render(t, 0); unset != v1 {
		t.Errorf("an unset major renders differently from v1:\n%s\n---\n%s", unset, v1)
	}
	const v1Plugin = "  - remote: buf.build/connectrpc/go\n    out: gen\n"
	if !strings.Contains(v1, v1Plugin) {
		t.Errorf("v1 buf.gen.yaml lost the unversioned connect-go plugin:\n%s", v1)
	}
	v2 := render(t, connectmajor.V2)
	if want := "  - remote: buf.build/connectrpc/go:" + connectmajor.V2ScaffoldVersion + "\n    out: gen\n"; !strings.Contains(v2, want) {
		t.Errorf("v2 buf.gen.yaml does not pin %q:\n%s", want, v2)
	}
	if strings.Replace(v2, ":"+connectmajor.V2ScaffoldVersion, "", 1) != v1 {
		t.Errorf("v2 differs from v1 beyond the plugin pin:\n%s", v2)
	}
}
