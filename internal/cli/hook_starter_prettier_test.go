package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/templates"
)

// hookStarterShapes covers every branch hooks.test.tsx.tmpl renders: a
// query-only service (no waitForSettled helper), a mutation-only one, a mixed
// one, and the control-plane service the defect was reported against.
//
// Names long enough to push the renderHook call past printWidth are NOT here,
// on purpose: where prettier wraps that line depends on the name and the
// project's config, so no template text can be clean for them. The scaffold
// hands those to the frontend's own prettier instead
// (TestWriteHookStarterTest_RunsTheFrontendsOwnPrettier). The blank-line
// guard still renders a long-name shape, since blank lines do not depend on
// width.
func hookStarterShapes() map[string]codegen.ServiceDef {
	return map[string]codegen.ServiceDef{
		"query-only": {Name: "AuditService", Methods: []codegen.Method{
			{Name: "GetAuditLog", InputType: "GetAuditLogRequest", OutputType: "GetAuditLogResponse"},
			{Name: "ListAuditLogs", InputType: "ListAuditLogsRequest", OutputType: "ListAuditLogsResponse"},
		}},
		"mutation-only": {Name: "MailerService", Methods: []codegen.Method{
			{Name: "SendEmail", InputType: "SendEmailRequest", OutputType: "SendEmailResponse"},
		}},
		"mixed": codegenServiceDefForStarterTest(),
		// The control-plane service the defect was reported against.
		"control-plane": {Name: "GitCredentialInternalService", Methods: []codegen.Method{
			{Name: "GetUserAccessToken", InputType: "GetUserAccessTokenRequest", OutputType: "GetUserAccessTokenResponse"},
		}},
	}
}

// longNameHookStarterShape crosses printWidth on purpose; see
// hookStarterShapes for why it is checked for blank lines only.
func longNameHookStarterShape() codegen.ServiceDef {
	return codegen.ServiceDef{Name: "GitCredentialInternalAdministrationService", Methods: []codegen.Method{
		{Name: "GetUserAccessTokenForProviderInstallation", InputType: "GetUserAccessTokenForProviderInstallationRequest", OutputType: "GetUserAccessTokenForProviderInstallationResponse"},
		{Name: "RotateProviderInstallationCredentialsForOrganization", InputType: "RotateProviderInstallationCredentialsForOrganizationRequest", OutputType: "RotateProviderInstallationCredentialsForOrganizationResponse"},
	}}
}

func renderHookStarters(t *testing.T, shapes map[string]codegen.ServiceDef) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, svc := range shapes {
		dir := t.TempDir()
		fileName := strings.ToLower(name) + "-service-hooks_gen.ts"
		if err := writeHookStarterTest(dir, fileName, svc, codegen.ServiceDefToHookData(svc)); err != nil {
			t.Fatalf("%s: writeHookStarterTest: %v", name, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(fileName, "_gen.ts")+".test.tsx"))
		if err != nil {
			t.Fatalf("%s: read rendered starter: %v", name, err)
		}
		out[name] = string(b)
	}
	return out
}

// TestHookStarterTest_BlankLinesArePrettierShaped is the -short guard for the
// defect control-plane hit: the template's {{range}} / {{if}} actions left a
// blank line after every block opener and before every block closer, which
// prettier deletes, so a fresh service's starter test failed the project's
// own prettier check in CI.
//
// Prettier's rule for blank lines in TS is mechanical, so it is checkable
// without node: never two in a row, never one right after a line that opens a
// block, never one right before a line that closes it. The real prettier run
// is TestHookStarterTest_PrettierClean.
func TestHookStarterTest_BlankLinesArePrettierShaped(t *testing.T) {
	shapes := hookStarterShapes()
	shapes["long-names"] = longNameHookStarterShape()
	for name, src := range renderHookStarters(t, shapes) {
		lines := strings.Split(src, "\n")
		for i := 1; i < len(lines)-1; i++ {
			if strings.TrimSpace(lines[i]) != "" {
				continue
			}
			prev := strings.TrimRight(lines[i-1], " \t")
			next := strings.TrimSpace(lines[i+1])
			switch {
			case strings.TrimSpace(prev) == "":
				t.Errorf("%s: line %d: two blank lines in a row (prettier collapses them)", name, i+1)
			case strings.HasSuffix(prev, "{"), strings.HasSuffix(prev, "("), strings.HasSuffix(prev, "=> {"):
				t.Errorf("%s: line %d: blank line right after a block opener %q (prettier deletes it)", name, i+1, strings.TrimSpace(prev))
			case strings.HasPrefix(next, "}"), strings.HasPrefix(next, ")"):
				t.Errorf("%s: line %d: blank line right before a block closer %q (prettier deletes it)", name, i+1, next)
			}
		}
		if !strings.HasSuffix(src, "\n") || strings.HasSuffix(src, "\n\n") {
			t.Errorf("%s: file must end in exactly one newline", name)
		}
	}
}

// TestHookStarterTest_PrettierClean runs the prettier a scaffolded frontend
// installs, with the prettier.config.js it ships, over every rendered shape:
// `prettier --check` must accept each file unchanged. This is the check
// control-plane's CI ran against git-credential-internal-service-hooks.test.tsx.
func TestHookStarterTest_PrettierClean(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the prettier run (downloads prettier on first use); TestHookStarterTest_BlankLinesArePrettierShaped still guards the blank-line shape")
	}
	npx := requireNpxForPrettier(t)

	dir := t.TempDir()
	// The scaffold's own prettier config, so printWidth and friends are the
	// ones a real project formats against.
	cfg, err := templates.FrontendTemplates().Get("nextjs/prettier.config.js")
	if err != nil {
		t.Fatalf("read scaffolded prettier config: %v", err)
	}
	// .mjs: the scaffolded file is an ES module and a bare temp dir has no
	// package.json declaring "type": "module".
	if err := os.WriteFile(filepath.Join(dir, "prettier.config.mjs"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, src := range renderHookStarters(t, hookStarterShapes()) {
		p := filepath.Join(dir, name+"-service-hooks.test.tsx")
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	args := append([]string{"-y", "prettier@" + scaffoldedFrontendPrettierVersion, "--config", filepath.Join(dir, "prettier.config.mjs"), "--check"}, paths...)
	cmd := exec.Command(npx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		var diffs strings.Builder
		for _, p := range paths {
			formatted, ferr := exec.Command(npx, "-y", "prettier@"+scaffoldedFrontendPrettierVersion,
				"--config", filepath.Join(dir, "prettier.config.mjs"), p).Output()
			orig, _ := os.ReadFile(p)
			if ferr == nil && string(formatted) != string(orig) {
				diffs.WriteString("\n--- " + filepath.Base(p) + " ---\n" + firstLineDiffs(string(orig), string(formatted)))
			}
		}
		t.Fatalf("prettier rewrites the scaffolded hook test, so a fresh service fails the project's prettier check (%v):\n%s%s", err, out, diffs.String())
	}
}

// TestWriteHookStarterTest_RunsTheFrontendsOwnPrettier pins the second half
// of the fix. No template text is prettier-stable for every name length and
// every project prettier config — where prettier wraps a long renderHook call
// depends on both — so the file forge just scaffolded is handed to the
// frontend's own installed prettier.
//
// The prettier here is a stub on node_modules/.bin that records its argv and
// rewrites the file, which proves the three properties that matter without
// node: it is FOUND by walking up from the hooks dir, it is invoked on the
// one file forge created (--write <file>), and its output is what lands.
func TestWriteHookStarterTest_RunsTheFrontendsOwnPrettier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub prettier is a POSIX shell script")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	feDir := filepath.Join(root, "frontends", "web")
	hooksDir := filepath.Join(feDir, "src", "hooks")
	binDir := filepath.Join(feDir, "node_modules", ".bin")
	for _, d := range []string{hooksDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	argvLog := filepath.Join(root, "prettier-argv")
	stub := "#!/bin/sh\necho \"$@\" > '" + argvLog + "'\nfor a in \"$@\"; do f=\"$a\"; done\nprintf 'formatted by the project prettier\\n' > \"$f\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "prettier"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	svc := codegenServiceDefForStarterTest()
	if err := writeHookStarterTest(hooksDir, "user-service-hooks_gen.ts", svc, codegen.ServiceDefToHookData(svc)); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the frontend's node_modules/.bin/prettier was never invoked: %v", err)
	}
	if !strings.Contains(string(argv), "--write") || !strings.Contains(string(argv), "user-service-hooks.test.tsx") {
		t.Errorf("prettier should be run with --write on the scaffolded file, got argv: %s", argv)
	}
	got, err := os.ReadFile(filepath.Join(hooksDir, "user-service-hooks.test.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "formatted by the project prettier\n" {
		t.Errorf("the formatter's output should be what lands on disk, got:\n%s", got)
	}
}

// Without an installed prettier (a fresh scaffold before `npm install`) the
// template output stands, and nothing is fetched: there is no npx fallback.
func TestWriteHookStarterTest_NoInstalledPrettierKeepsTemplateOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(root, "frontends", "web", "src", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := codegenServiceDefForStarterTest()
	if err := writeHookStarterTest(hooksDir, "user-service-hooks_gen.ts", svc, codegen.ServiceDefToHookData(svc)); err != nil {
		t.Fatalf("writeHookStarterTest: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(hooksDir, "user-service-hooks.test.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `describe("UserService hooks"`) {
		t.Errorf("template output should be kept when no prettier is installed:\n%s", got)
	}
}

// requireNpxForPrettier returns npx, or skips when node is absent on a
// developer machine — and FAILS under CI (or FORGE_E2E_REQUIRE_TOOLS=1),
// where a missing node is a provisioning bug, not a reason to report green
// for a check that never ran. Shaped like requireTool (e2e-tagged, so not
// callable here): the skip is the guarded branch and the failure the
// fall-through, which is what internal/vacuousguard reads.
func requireNpxForPrettier(t *testing.T) string {
	t.Helper()
	npx, lookErr := exec.LookPath("npx")
	if lookErr != nil && os.Getenv("CI") == "" && os.Getenv("FORGE_E2E_REQUIRE_TOOLS") == "" {
		t.Skip("npx (node) not on PATH: cannot run prettier — skipped locally, a hard failure under CI; TestHookStarterTest_BlankLinesArePrettierShaped still guards the blank-line shape")
	}
	if lookErr != nil {
		t.Fatalf("npx (node) not on PATH under CI: install node in the job so the prettier check actually runs")
	}
	return npx
}

// scaffoldedFrontendPrettierVersion is the prettier the scaffolded
// frontends' package.json pins (templates.PrettierVersion).
const scaffoldedFrontendPrettierVersion = templates.PrettierVersion

// firstLineDiffs lists up to eight line pairs prettier rewrites. It compares
// line-by-line, so it is only a pointer into the file once line counts
// diverge — enough to show WHERE the template drifted.
func firstLineDiffs(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out strings.Builder
	shown := 0
	for i := 0; i < len(al) && i < len(bl) && shown < 8; i++ {
		if al[i] != bl[i] {
			out.WriteString("  line " + strconv.Itoa(i+1) + ":\n    - " + al[i] + "\n    + " + bl[i] + "\n")
			shown++
		}
	}
	return out.String()
}
