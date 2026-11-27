package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestV190Migration_Detection runs the SHIPPED v1.9.0 detection against one
// shape per section it covers (offered) and the replacement each section names
// (not offered), so a detection that stops matching a removed spelling, or
// starts matching its replacement, fails here instead of in a consumer's
// `forge project upgrade list`.
func TestV190Migration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.SkillPath == "migrations/v1.9.0" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped migration at migrations/v1.9.0")
	}

	type file struct{ rel, body string }
	for name, tc := range map[string]struct {
		files []file
		want  bool
	}{
		"an empty project": {nil, false},
		// Section 1: removed forge/pkg APIs.
		"imports pkg/approval":          {[]file{{"internal/x/x.go", "import \"github.com/reliant-labs/forge/pkg/approval\"\n"}}, true},
		"imports root pkg/appkit":       {[]file{{"internal/app/lifecycle.go", "import \"github.com/reliant-labs/forge/pkg/appkit\"\n"}}, true},
		"imports appkit/operatorkit":    {[]file{{"internal/app/lifecycle.go", "import \"github.com/reliant-labs/forge/pkg/appkit/operatorkit\"\n"}}, false},
		"calls contractkit.LogCall":     {[]file{{"internal/x/x.go", "defer contractkit.LogCall(ctx, logger, \"X\")\n"}}, true},
		"calls observe.LogCall":         {[]file{{"internal/x/x.go", "defer observe.LogCall(ctx, logger, \"X\", start, err)\n"}}, false},
		"sets ProofLocker":              {[]file{{"internal/app/login_broker.go", "cfg := devidp.SignInRoutesConfig{ProofLocker: l}\n"}}, true},
		"uses obsquery.ActiveEvidence":  {[]file{{"internal/x/x.go", "q := obsquery.ActiveEvidence + \" AND x\"\n"}}, true},
		"mentions it in a comment only": {[]file{{"internal/x/x.go", "// dashboard compiles (obsquery.ActiveEvidence: the Users dashboard)\n"}}, false},
		"uses ServerCulpritSQL (kept)":  {[]file{{"internal/x/x.go", "expr := obsquery.ServerCulpritSQL\n"}}, false},
		"reads ShapeDiff.KindChanged":   {[]file{{"internal/x/x.go", "if d.KindChanged != nil {}\n"}}, true},
		// Section 2: the retired proto options.
		"a (forge.v1.entity) option": {[]file{{"proto/services/a/v1/a.proto", "message A {\n  option (forge.v1.entity) = {table: \"a\"};\n}\n"}}, true},
		// Section 3: removed flags and env vars.
		"a script passing --reliant-managed": {[]file{{"internal/diskwatch/reap.go", "args := []string{\"forge\", \"storage\", \"worktrees\", \"--apply\", \"--reliant-managed\"}\n"}}, true},
		"FORGE_NO_HOOKS in a script":         {[]file{{"scripts/pin.sh", "FORGE_NO_HOOKS=1 forge generate\n"}}, true},
		"FORGE_NO_GIT_HOOKS in a script":     {[]file{{"scripts/pin.sh", "FORGE_NO_GIT_HOOKS=1 forge generate\n"}}, false},
		"forge build --push in a Taskfile":   {[]file{{"Taskfile.yml", "  build:\n    cmds:\n      - forge build --push\n"}}, true},
		// Section 4: retired forge.yaml keys.
		"forge.yaml version key": {[]file{{"forge.yaml", "name: app\nversion: 0.1.0\n"}}, true},
		"forge.yaml without one": {[]file{{"forge.yaml", "name: app\nforge_version: v1.9.0\n"}}, false},
		// Section 5: KCL.
		"env-wide replay (control-plane's merge)": {[]file{{"deploy/kcl/prod/main.k", "    observability = (o | {signals = o.signals | {replay = True}})\n"}}, true},
		"env-wide replay on ObservabilitySignals": {[]file{{"deploy/kcl/prod/main.k", "        signals = forge.ObservabilitySignals {replay = True}\n"}}, true},
		"per-frontend replay":                     {[]file{{"deploy/kcl/prod/main.k", "    telemetry = forge.BrowserTelemetry {replay = True}\n"}}, false},
		// Section 6: root_gen.go.
		"root.go calling generatedCommands": {[]file{{"cmd/app/cmd/root.go", "for _, c := range generatedCommands(deps) {\n"}}, true},
		"root.go on registerCommand":        {[]file{{"cmd/app/cmd/root.go", "for _, f := range registeredCommands {\n"}}, false},
		// Section 8: web runtime attribution.
		"attribution: false": {[]file{{"src/lib/monitoring.ts", "initBrowserTelemetry({ attribution: false });\n"}}, true},
		"analytics: full":    {[]file{{"src/lib/monitoring.ts", "initBrowserTelemetry({ analytics: \"full\" });\n"}}, false},
		// Section 9: ledger formats.
		"a .forge/scaffolded.json ledger": {[]file{{".forge/scaffolded.json", "{}\n"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				path := filepath.Join(dir, filepath.FromSlash(f.rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := migrationApplies(m, "v1.8.0", dir); got != tc.want {
				t.Errorf("migrationApplies = %v, want %v", got, tc.want)
			}
		})
	}
}
