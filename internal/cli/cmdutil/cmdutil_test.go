package cmdutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The route/override tests mutate process-global state, so none of them run in
// parallel and each restores what it changed.

func TestCmdRoute_RecordsMountTokens(t *testing.T) {
	t.Cleanup(ResetCmdRoute)

	tests := []struct {
		name       string
		build      func() *cobra.Command // returns forge's root in the executing tree
		wantTokens []string
	}{
		{
			name:       "standalone binary has no tokens",
			build:      func() *cobra.Command { return &cobra.Command{Use: "forge"} },
			wantTokens: nil,
		},
		{
			name: "mounted under another root records the subcommand",
			build: func() *cobra.Command {
				host := &cobra.Command{Use: "reliant"}
				forge := &cobra.Command{Use: "forge"}
				host.AddCommand(forge)
				return forge
			},
			wantTokens: []string{"forge"},
		},
		{
			name: "nested mount records every hop in order",
			build: func() *cobra.Command {
				host := &cobra.Command{Use: "tool"}
				mid := &cobra.Command{Use: "dev"}
				forge := &cobra.Command{Use: "forge"}
				host.AddCommand(mid)
				mid.AddCommand(forge)
				return forge
			},
			wantTokens: []string{"dev", "forge"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetCmdRoute()
			if _, ok := CmdRouteTokens(); ok {
				t.Fatal("route reported as recorded after ResetCmdRoute")
			}

			RecordCmdRoute(tc.build())

			got, ok := CmdRouteTokens()
			if !ok {
				t.Fatal("route not reported as recorded after RecordCmdRoute")
			}
			if strings.Join(got, " ") != strings.Join(tc.wantTokens, " ") {
				t.Fatalf("tokens = %v, want %v", got, tc.wantTokens)
			}
		})
	}
}

func TestName_UsesRecordedRouteElseBasenameHeuristic(t *testing.T) {
	t.Cleanup(ResetCmdRoute)
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	tests := []struct {
		name string
		arg0 string
		// record, when non-nil, is passed to RecordCmdRoute before calling Name.
		record func() *cobra.Command
		want   string
	}{
		{
			name: "no recording and binary named forge",
			arg0: "/usr/local/bin/forge",
			want: "forge",
		},
		{
			name: "no recording and a differently named binary assumes it embeds forge",
			arg0: "/usr/local/bin/reliant",
			want: "reliant forge",
		},
		{
			name:   "recorded standalone route reports the basename even when it is not 'forge'",
			arg0:   "/tmp/forge-v2",
			record: func() *cobra.Command { return &cobra.Command{Use: "forge"} },
			want:   "forge-v2",
		},
		{
			name: "recorded embedded route reports basename plus mount tokens",
			arg0: "/usr/bin/reliant",
			record: func() *cobra.Command {
				host := &cobra.Command{Use: "reliant"}
				forge := &cobra.Command{Use: "forge"}
				host.AddCommand(forge)
				return forge
			},
			want: "reliant forge",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetCmdRoute()
			os.Args = []string{tc.arg0}
			if tc.record != nil {
				RecordCmdRoute(tc.record())
			}
			if got := Name(); got != tc.want {
				t.Fatalf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetProjectDir(t *testing.T) {
	t.Cleanup(func() { _ = SetProjectDir("") })
	dir := t.TempDir()
	file := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("existing directory is installed as an absolute override", func(t *testing.T) {
		if err := SetProjectDir(dir); err != nil {
			t.Fatalf("SetProjectDir(%q): %v", dir, err)
		}
		got, ok := ProjectDirOverride()
		if !ok || got != dir {
			t.Fatalf("ProjectDirOverride() = (%q, %v), want (%q, true)", got, ok, dir)
		}
		root, err := ResolutionRoot()
		if err != nil || root != dir {
			t.Fatalf("ResolutionRoot() = (%q, %v), want %q", root, err, dir)
		}
	})

	t.Run("relative directory resolves against the cwd", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(cwd, dir)
		if err != nil {
			t.Skipf("no relative path from %s to %s: %v", cwd, dir, err)
		}
		if err := SetProjectDir(rel); err != nil {
			t.Fatalf("SetProjectDir(%q): %v", rel, err)
		}
		if got, _ := ProjectDirOverride(); got != dir {
			t.Fatalf("override = %q, want absolute %q", got, dir)
		}
	})

	t.Run("empty clears the override and falls back to the cwd", func(t *testing.T) {
		if err := SetProjectDir(dir); err != nil {
			t.Fatal(err)
		}
		if err := SetProjectDir(""); err != nil {
			t.Fatal(err)
		}
		if got, ok := ProjectDirOverride(); ok {
			t.Fatalf("override still set: %q", got)
		}
		cwd, _ := os.Getwd()
		if root, err := ResolutionRoot(); err != nil || root != cwd {
			t.Fatalf("ResolutionRoot() = (%q, %v), want cwd %q", root, err, cwd)
		}
	})

	t.Run("a missing directory is rejected and leaves the override untouched", func(t *testing.T) {
		if err := SetProjectDir(dir); err != nil {
			t.Fatal(err)
		}
		err := SetProjectDir(filepath.Join(dir, "nope"))
		if err == nil || !strings.Contains(err.Error(), "project dir") {
			t.Fatalf("err = %v, want a 'project dir' error", err)
		}
		if got, _ := ProjectDirOverride(); got != dir {
			t.Fatalf("a failed SetProjectDir changed the override to %q", got)
		}
	})

	t.Run("a regular file is rejected", func(t *testing.T) {
		err := SetProjectDir(file)
		if err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("err = %v, want 'is not a directory'", err)
		}
	})
}

func TestProjectRoot_And_FindProjectRoot(t *testing.T) {
	t.Cleanup(func() { _ = SetProjectDir("") })

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "forge.yaml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(project, "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory with no forge.yaml anywhere above it inside the sandbox is
	// not guaranteed (the temp root may sit under one), so only assert the
	// "none found" branch through a real filesystem root check below.

	t.Run("ProjectRoot returns the resolution root when forge.yaml is there", func(t *testing.T) {
		if err := SetProjectDir(project); err != nil {
			t.Fatal(err)
		}
		got, err := ProjectRoot()
		if err != nil || got != project {
			t.Fatalf("ProjectRoot() = (%q, %v), want %q", got, err, project)
		}
	})

	t.Run("ProjectRoot does not walk up", func(t *testing.T) {
		if err := SetProjectDir(nested); err != nil {
			t.Fatal(err)
		}
		_, err := ProjectRoot()
		if err == nil {
			t.Fatal("ProjectRoot() succeeded from a subdirectory; it must not walk up")
		}
		if !strings.Contains(err.Error(), "forge.yaml not found") {
			t.Fatalf("err = %q, want it to say forge.yaml was not found", err)
		}
	})

	t.Run("FindProjectRoot walks up to the directory holding forge.yaml", func(t *testing.T) {
		if err := SetProjectDir(nested); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot()
		if err != nil || got != project {
			t.Fatalf("FindProjectRoot() = (%q, %v), want %q", got, err, project)
		}
	})

	t.Run("FindProjectRoot returns empty when no ancestor has forge.yaml", func(t *testing.T) {
		if _, err := os.Stat("/forge.yaml"); err == nil {
			t.Skip("host has /forge.yaml")
		}
		bare := t.TempDir()
		for d := bare; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "forge.yaml")); err == nil {
				t.Fatalf("test requires a temp dir with no project ancestor; %s holds a forge.yaml", d)
			}
			if filepath.Dir(d) == d {
				break
			}
		}
		if err := SetProjectDir(bare); err != nil {
			t.Fatal(err)
		}
		got, err := FindProjectRoot()
		if err != nil || got != "" {
			t.Fatalf("FindProjectRoot() = (%q, %v), want (\"\", nil)", got, err)
		}
	})
}

func TestConfiguresClone(t *testing.T) {
	newRun := func(annotate bool, readOnly ...string) (parent, child *cobra.Command) {
		parent = &cobra.Command{Use: "generate"}
		parent.Flags().Bool("check", false, "")
		parent.Flags().Bool("plan", false, "")
		parent.Flags().Bool(DryRunFlag, false, "")
		child = &cobra.Command{Use: "sub"}
		child.Flags().Bool("check", false, "")
		child.Flags().Bool(DryRunFlag, false, "")
		parent.AddCommand(child)
		if annotate {
			if got := ConfiguresClone(parent, readOnly...); got != parent {
				t.Fatal("ConfiguresClone must return the command it was given")
			}
		}
		return parent, child
	}
	set := func(c *cobra.Command, flag string) {
		t.Helper()
		if err := c.Flags().Set(flag, "true"); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unannotated command never configures the clone", func(t *testing.T) {
		parent, child := newRun(false)
		if ConfiguresCloneFor(parent) || ConfiguresCloneFor(child) {
			t.Fatal("an unannotated command must not configure the clone")
		}
	})

	t.Run("annotated command and its subcommands do", func(t *testing.T) {
		parent, child := newRun(true)
		if !ConfiguresCloneFor(parent) {
			t.Fatal("annotated command should configure the clone")
		}
		if !ConfiguresCloneFor(child) {
			t.Fatal("subcommand of an annotated command should configure the clone")
		}
	})

	t.Run("a listed read-only flag turns it off", func(t *testing.T) {
		parent, _ := newRun(true, "check", "plan")
		set(parent, "plan")
		if ConfiguresCloneFor(parent) {
			t.Fatal("--plan is declared read-only but the clone was still configured")
		}
	})

	t.Run("a listed flag that is merely false does not", func(t *testing.T) {
		parent, _ := newRun(true, "check")
		if err := parent.Flags().Set("check", "false"); err != nil {
			t.Fatal(err)
		}
		if !ConfiguresCloneFor(parent) {
			t.Fatal("--check=false is not a read-only run")
		}
	})

	t.Run("an unlisted flag does not make the run read-only", func(t *testing.T) {
		parent, _ := newRun(true, "plan")
		set(parent, "check")
		if !ConfiguresCloneFor(parent) {
			t.Fatal("--check is not listed, so the run still configures the clone")
		}
	})

	t.Run("a listed flag the command does not define is ignored", func(t *testing.T) {
		parent, _ := newRun(true, "no-such-flag")
		if !ConfiguresCloneFor(parent) {
			t.Fatal("an unknown read-only flag name must not disable setup")
		}
	})

	t.Run("--dry-run overrides the annotation", func(t *testing.T) {
		parent, child := newRun(true)
		set(parent, DryRunFlag)
		if !IsDryRun(parent) {
			t.Fatal("IsDryRun false with --dry-run set")
		}
		if ConfiguresCloneFor(parent) {
			t.Fatal("a dry run must never configure the clone")
		}
		if IsDryRun(child) {
			t.Fatal("dry-run on the parent must not leak to the child's flag set")
		}
	})

	t.Run("IsDryRun is false without the flag or when unchanged", func(t *testing.T) {
		if IsDryRun(&cobra.Command{Use: "bare"}) {
			t.Fatal("a command without --dry-run cannot be a dry run")
		}
		parent, _ := newRun(false)
		if IsDryRun(parent) {
			t.Fatal("an unset --dry-run is not a dry run")
		}
	})
}

func TestHideDevFlags(t *testing.T) {
	newCmd := func(ran *bool) *cobra.Command {
		cmd := &cobra.Command{Use: "lint", RunE: func(*cobra.Command, []string) error { *ran = true; return nil }}
		cmd.Flags().Bool("visible", false, "a user flag")
		cmd.Flags().Bool("wiring-audit", false, "audit the wiring")
		cmd.Flags().String("only", "", "narrow the pipeline")
		cmd.Flags().String("old", "", "legacy knob")
		_ = cmd.Flags().MarkDeprecated("old", "use --only")
		return cmd
	}

	t.Run("named flags are hidden, others stay visible", func(t *testing.T) {
		var ran bool
		cmd := newCmd(&ran)
		HideDevFlags(cmd, "wiring-audit", "only")
		for name, wantHidden := range map[string]bool{"wiring-audit": true, "only": true, "visible": false} {
			if got := cmd.Flags().Lookup(name).Hidden; got != wantHidden {
				t.Errorf("flag %s hidden = %v, want %v", name, got, wantHidden)
			}
		}
		if cmd.Flags().Lookup("help-dev") == nil {
			t.Fatal("--help-dev was not registered")
		}
	})

	t.Run("without --help-dev the original RunE runs", func(t *testing.T) {
		var ran bool
		cmd := newCmd(&ran)
		HideDevFlags(cmd, "only")
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatal(err)
		}
		if !ran {
			t.Fatal("the wrapped RunE was never invoked")
		}
	})

	t.Run("--help-dev lists exactly the hidden flags and skips the run", func(t *testing.T) {
		var ran bool
		cmd := newCmd(&ran)
		HideDevFlags(cmd, "wiring-audit", "only", "old")
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Flags().Set("help-dev", "true"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatal(err)
		}
		if ran {
			t.Fatal("--help-dev must not run the command")
		}
		got := out.String()
		for _, want := range []string{
			`Maintainer/debug flags for "forge lint"`,
			"--wiring-audit\n          audit the wiring",
			"--only string\n          narrow the pipeline",
			"legacy knob (DEPRECATED: use --only)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("--help-dev output missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "a user flag") {
			t.Errorf("--help-dev listed a visible flag:\n%s", got)
		}
	})

	t.Run("a command with no RunE is a no-op rather than a nil deref", func(t *testing.T) {
		cmd := &cobra.Command{Use: "x"}
		cmd.Flags().Bool("dev", false, "")
		HideDevFlags(cmd, "dev")
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("RunE = %v, want nil", err)
		}
	})

	t.Run("an unknown flag name is a construction-time panic", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected a panic for an unknown flag")
			}
			if msg, _ := r.(string); !strings.Contains(msg, "HideDevFlags(lint)") {
				t.Fatalf("panic = %v, want it to name the command", r)
			}
		}()
		var ran bool
		HideDevFlags(newCmd(&ran), "typo")
	})
}

func TestStrictGroup(t *testing.T) {
	build := func() (*cobra.Command, *bytes.Buffer) {
		root := &cobra.Command{Use: "forge"}
		group := StrictGroup(&cobra.Command{Use: "database", Short: "db stuff"})
		group.AddCommand(&cobra.Command{Use: "migrate", Run: func(*cobra.Command, []string) {}})
		group.AddCommand(&cobra.Command{Use: "seed", Run: func(*cobra.Command, []string) {}})
		root.AddCommand(group)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		return root, &out
	}

	t.Run("bare group prints help and succeeds", func(t *testing.T) {
		root, out := build()
		root.SetArgs([]string{"database"})
		if err := root.Execute(); err != nil {
			t.Fatalf("bare group failed: %v", err)
		}
		if !strings.Contains(out.String(), "migrate") || !strings.Contains(out.String(), "seed") {
			t.Fatalf("help did not list the subcommands:\n%s", out)
		}
	})

	t.Run("unknown subcommand fails naming the token and suggests a near miss", func(t *testing.T) {
		root, _ := build()
		root.SetArgs([]string{"database", "migrat"})
		err := root.Execute()
		if err == nil {
			t.Fatal("unknown subcommand was accepted")
		}
		msg := err.Error()
		for _, want := range []string{`unknown command "migrat" for "forge database"`, "Did you mean this?", "migrate"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("a typo within edit distance 2 is suggested even when it is not a prefix", func(t *testing.T) {
		root, _ := build()
		root.SetArgs([]string{"database", "migrtae"}) // transposed letters: not a prefix of "migrate"
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "Did you mean this?") || !strings.Contains(err.Error(), "migrate") {
			t.Fatalf("err = %v, want a suggestion for migrate", err)
		}
	})

	t.Run("unknown subcommand with nothing close gets no suggestion", func(t *testing.T) {
		root, _ := build()
		root.SetArgs([]string{"database", "zzzzzzzz"})
		err := root.Execute()
		if err == nil || strings.Contains(err.Error(), "Did you mean") {
			t.Fatalf("err = %v, want an error without a suggestion", err)
		}
	})

	t.Run("suggestions disabled on the group are honoured", func(t *testing.T) {
		root, _ := build()
		root.Commands()[0].DisableSuggestions = true
		root.SetArgs([]string{"database", "migrat"})
		err := root.Execute()
		if err == nil || strings.Contains(err.Error(), "Did you mean") {
			t.Fatalf("err = %v, want an error without a suggestion", err)
		}
	})
}

func TestAddPackageScaffoldFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "package"}
	AddPackageScaffoldFlags(cmd)
	for name, wantType := range map[string]string{"base-url": "bool", "base-url-env": "string", "dry-run": "bool"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s not registered", name)
		}
		if f.Value.Type() != wantType {
			t.Errorf("--%s type = %s, want %s", name, f.Value.Type(), wantType)
		}
		if f.DefValue != "false" && f.DefValue != "" {
			t.Errorf("--%s default = %q, want zero value", name, f.DefValue)
		}
	}
	if err := cmd.ParseFlags([]string{"--base-url-env", "PAYMENTS_URL", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cmd.Flags().GetString("base-url-env"); got != "PAYMENTS_URL" {
		t.Errorf("base-url-env = %q", got)
	}
	if !IsDryRun(cmd) {
		t.Error("--dry-run registered by AddPackageScaffoldFlags is not recognised by IsDryRun")
	}
}

func TestCountLineStartScaffoldMarkers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"go marker", "// FORGE_SCAFFOLD: fill in\n", 1},
		{"hash marker", "# FORGE_SCAFFOLD: fill in\n", 1},
		{"indented markers still count", "\t// FORGE_SCAFFOLD: a\n  # FORGE_SCAFFOLD: b\n", 2},
		{"marker mid-line is prose, not a placeholder", "x := 1 // FORGE_SCAFFOLD: nope\n", 0},
		{"wrong casing does not count", "// forge_scaffold: x\n", 0},
		{"missing colon does not count", "// FORGE_SCAFFOLD fill in\n", 0},
		{"mixed file", "package x\n// FORGE_SCAFFOLD: one\nfunc f() {}\n// FORGE_SCAFFOLD: two\n", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountLineStartScaffoldMarkers([]byte(tc.in)); got != tc.want {
				t.Fatalf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIsMarkerScannable(t *testing.T) {
	tests := map[string]bool{
		"main.go": true, "svc.proto": true, "page.tsx": true, "a.ts": true, "a.js": true, "a.jsx": true,
		"forge.yaml": true, "x.yml": true, "001.sql": true, "main.k": true, "run.sh": true,
		"a.tmpl": true, "x.toml": true,
		"MAIN.GO":   true, // extension match is case-insensitive
		"README.md": false, "data.json": false, "logo.png": false, "Makefile": false, "": false,
	}
	for name, want := range tests {
		if got := IsMarkerScannable(name); got != want {
			t.Errorf("IsMarkerScannable(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // substring; "" means valid
	}{
		{"plain", "billing", ""},
		{"hyphen and underscore allowed", "admin-server_v2", ""},
		{"unicode letters allowed", "café", ""},
		{"empty", "", "cannot be empty"},
		{"leading digit", "1abc", "must start with a letter"},
		{"leading underscore", "_abc", "must start with a letter"},
		{"trailing hyphen", "abc-", "cannot end with a hyphen"},
		{"space", "a b", "invalid character"},
		{"dot", "a.b", "invalid character"},
		{"go keyword", "func", `"func" is a Go keyword`},
		{"predeclared", "string", `"string" is a Go predeclared identifier`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertErrContains(t, ValidateIdentifier(tc.in), tc.wantErr)
		})
	}
}

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"plain", "my-app", ""},
		{"trailing hyphen is fine for a project", "my-app-", ""},
		{"empty", "", "cannot be empty"},
		{"leading digit", "9lives", "must start with a letter"},
		{"slash", "a/b", "invalid character: /"},
		{"keyword", "go", `"go" is a Go keyword`},
		{"predeclared", "error", `"error" is a Go predeclared identifier`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertErrContains(t, ValidateProjectName(tc.in), tc.wantErr)
		})
	}
}

func TestValidateFrontendName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"plain", "web", ""},
		{"digits and hyphen", "admin-2", ""},
		{"empty", "", "cannot be empty"},
		{"path separator", "a/b", "invalid filesystem characters"},
		{"backslash", `a\b`, "invalid filesystem characters"},
		{"colon", "a:b", "invalid filesystem characters"},
		{"glob star", "a*", "invalid filesystem characters"},
		{"space", "my app", "cannot contain spaces"},
		{"leading dot", ".hidden", "cannot start with . or -"},
		{"leading hyphen", "-flag", "cannot start with . or -"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertErrContains(t, ValidateFrontendName(tc.in), tc.wantErr)
		})
	}
}

func TestReservedNameAlternatives(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"job", "workorder"},
		{"worker", "crew"},
		{"scheduler", "schedule"},
		{"cron", "schedule"},
		{"unlisted", "a noun naming what the service owns"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reservedNameAlternatives(tc.name); !strings.Contains(got, tc.want) {
				t.Fatalf("reservedNameAlternatives(%q) = %q, want it to contain %q", tc.name, got, tc.want)
			}
		})
	}
}

func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}
