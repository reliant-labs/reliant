package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/schemagen"
	"github.com/reliant-labs/forge/internal/vulnpatch"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

// patchHarness is vulnHarness plus a fake for every command the patcher
// runs (go, npm, the verify command, git, gh) and a controllable rescan.
type patchHarness struct {
	*vulnHarness
	dir string

	mu       sync.Mutex
	cmds     []string
	dirty    bool
	failSh   bool
	stillIn  map[string]bool // packages a directory rescan still reports
	dirScans []string
}

func newPatchHarness(t *testing.T) *patchHarness {
	t.Helper()
	h := &patchHarness{vulnHarness: newVulnHarness(t), stillIn: map[string]bool{}}
	h.dir = h.deps.projectDir()
	h.cut("v1.0.0", false)
	for name, body := range map[string]string{
		"forge.yaml":       "name: shop\n",
		"Taskfile.yml":     "version: '3'\ntasks:\n  test:short:\n    cmds: [go test -short ./...]\n",
		"go.mod":           "module example.com/shop\n\ngo 1.22\n",
		"web/package.json": "{\n  \"name\": \"web\",\n  \"dependencies\": {\n    \"react-router\": \"^7.14.0\"\n  }\n}\n",
		"web/package-lock.json": `{"lockfileVersion": 3, "packages": {
			"": {"name": "web"},
			"node_modules/react-router": {"version": "7.14.0"},
			"node_modules/ws": {"version": "8.20.0"}}}`,
	} {
		p := filepath.Join(h.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// grype answers a directory rescan with the packages stillIn names; every
// other grype and syft call is the vuln harness's.
func (h *patchHarness) grype(ctx context.Context, c vulnscan.Command) error {
	if len(c.Args) > 3 && strings.HasPrefix(c.Args[3], "dir:") {
		h.mu.Lock()
		h.dirScans = append(h.dirScans, strings.TrimPrefix(c.Args[3], "dir:"))
		still := h.stillIn
		h.mu.Unlock()
		var doc map[string]any
		if err := json.Unmarshal(h.sample, &doc); err != nil {
			return err
		}
		var kept []any
		for _, m := range doc["matches"].([]any) {
			if still[m.(map[string]any)["artifact"].(map[string]any)["name"].(string)] {
				kept = append(kept, m)
			}
		}
		doc["matches"] = kept
		doc["descriptor"].(map[string]any)["db"].(map[string]any)["status"].(map[string]any)["built"] = h.built.Format("2006-01-02T15:04:05Z07:00")
		return json.NewEncoder(c.Stdout).Encode(doc)
	}
	return h.run(ctx, c)
}

func (h *patchHarness) exec(ctx context.Context, c vulnscan.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rel, _ := filepath.Rel(h.dir, c.Dir)
	argv := strings.Join(append([]string{c.Path}, c.Args...), " ")
	if c.Path == "gh" {
		argv = "gh pr create --head " + c.Args[3] // the body file is a temp path
	}
	h.cmds = append(h.cmds, rel+": "+argv)
	switch {
	case argv == "git status --porcelain" && h.dirty:
		_, _ = io.WriteString(c.Stdout, " M go.mod\n")
	case c.Path == "git" && len(c.Args) > 1 && c.Args[0] == "rev-parse":
		_, _ = io.WriteString(c.Stdout, "abc1234\n")
	case c.Path == "gh":
		_, _ = io.WriteString(c.Stdout, "Creating pull request\nhttps://github.com/acme/shop/pull/7\n")
	case c.Path == "sh" && h.failSh:
		_, _ = io.WriteString(c.Stdout, "--- FAIL: TestCheckout\n")
		return errors.New("exit status 1")
	}
	return nil
}

func (h *patchHarness) patch(args ...string) (doc vulnpatch.Document, stdout, stderr string, code int) {
	h.t.Helper()
	deps := vulnPatchDeps{vulnDeps: h.deps, findingsFor: machineLedgerPatchFindings, exec: h.exec}
	deps.run = h.grype
	cmd := newVulnPatchCmdWith(deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append(args, "--json"))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	if err != nil {
		code = 1
		var ec *exitCodeError
		if errors.As(err, &ec) {
			code = ec.code
		}
		errOut.WriteString(err.Error())
	}
	if out.Len() > 0 {
		doc = decodePatchDoc(h.t, out.Bytes())
	}
	return doc, out.String(), errOut.String(), code
}

func decodePatchDoc(t *testing.T, out []byte) vulnpatch.Document {
	t.Helper()
	var doc vulnpatch.Document
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not a vuln-patch document: %v\n%s", err, out)
	}
	compiled, err := schemagen.Compile(vulnPatchSchemaJSON())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := schemagen.DecodeJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	if errs := compiled.Validate(inst); len(errs) > 0 {
		t.Fatalf("document does not validate against forge project schema vuln-patch: %v\n%s", errs, out)
	}
	return doc
}

func statuses(doc vulnpatch.Document) map[string]string {
	out := map[string]string{}
	for _, r := range doc.Findings {
		out[r.ID+" "+r.Package.Name] = string(r.Status)
	}
	return out
}

func TestVulnPatchEnvPatchesWhatItCanAndHandsTheRestOver(t *testing.T) {
	h := newPatchHarness(t)
	doc, _, stderr, code := h.patch("prod")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want 2 (the OS package has no patcher): %s", code, stderr)
	}
	want := map[string]string{
		"CVE-2026-4738 zlib":               "skipped",
		"GHSA-96hv-2xvq-fx4p ws":           "bumped",
		"GHSA-chx6-hx7r-mcp5 react-router": "bumped",
	}
	if got := statuses(doc); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if doc.Env != "prod" || doc.Release != "v1.0.0" || !doc.Changed || doc.ExitCode != 2 {
		t.Errorf("doc head = %s %s changed=%v exit=%d", doc.Env, doc.Release, doc.Changed, doc.ExitCode)
	}
	wantCmds := []string{"web: npm install", "web: npm install react-router@7.18.0"}
	if !reflect.DeepEqual(h.cmds, wantCmds) {
		t.Errorf("commands = %v, want %v", h.cmds, wantCmds)
	}
	pkg, _ := os.ReadFile(filepath.Join(h.dir, "web/package.json"))
	if !strings.Contains(string(pkg), `"overrides": {`+"\n"+`    "ws": "8.21.0"`) {
		t.Errorf("package.json:\n%s", pkg)
	}
	if len(h.installs) != 0 {
		t.Errorf("an env patch without --verify installed %v", h.installs)
	}

	// --ecosystem npm leaves the OS package out: everything requested is done.
	h = newPatchHarness(t)
	if doc, _, stderr, code = h.patch("prod", "--ecosystem", "npm"); code != 0 || len(doc.Findings) != 2 {
		t.Errorf("--ecosystem npm: exit %d findings %d: %s", code, len(doc.Findings), stderr)
	}
}

func TestVulnPatchVerify(t *testing.T) {
	h := newPatchHarness(t)
	doc, _, stderr, code := h.patch("prod", "GHSA-96hv-2xvq-fx4p", "--verify")
	if code != 0 || doc.Summary.Fixed != 1 {
		t.Fatalf("exit %d summary %+v: %s", code, doc.Summary, stderr)
	}
	if v := doc.Verify; v == nil || v.Command != "task test:short" || v.Source != vulnpatch.VerifySourceTaskfile || !v.Passed || !reflect.DeepEqual(v.Rescanned, []string{"web"}) {
		t.Errorf("verify = %+v", doc.Verify)
	}
	if want := filepath.Join(h.dir, "web"); len(h.dirScans) != 1 || h.dirScans[0] != want {
		t.Errorf("rescans = %v, want [%s]", h.dirScans, want)
	}
	if last := h.cmds[len(h.cmds)-1]; last != ".: sh -c task test:short" {
		t.Errorf("last command = %q", last)
	}

	// forge.yaml's vuln.patch.verify wins over the Taskfile; --verify=<cmd> over both.
	h = newPatchHarness(t)
	if err := os.WriteFile(filepath.Join(h.dir, "forge.yaml"), []byte("name: shop\nvuln:\n  patch:\n    verify: make check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc, _, _, _ = h.patch("prod", "GHSA-96hv-2xvq-fx4p", "--verify"); doc.Verify.Command != "make check" || doc.Verify.Source != "forge.yaml" {
		t.Errorf("forge.yaml verify = %+v", doc.Verify)
	}
	h = newPatchHarness(t)
	if doc, _, _, _ = h.patch("prod", "GHSA-96hv-2xvq-fx4p", "--verify=npm test"); doc.Verify.Command != "npm test" || doc.Verify.Source != "flag" {
		t.Errorf("--verify=cmd = %+v", doc.Verify)
	}

	// A failing gate is the agent takeover signal.
	h = newPatchHarness(t)
	h.failSh = true
	doc, _, stderr, code = h.patch("prod", "GHSA-96hv-2xvq-fx4p", "--verify")
	if code != exitWrong || doc.Verify.Passed || !strings.Contains(doc.Verify.Output, "FAIL: TestCheckout") || !strings.Contains(stderr, "verification failed") {
		t.Errorf("failing gate: exit %d verify %+v: %s", code, doc.Verify, stderr)
	}

	// So is a finding the rescan still reports.
	h = newPatchHarness(t)
	h.stillIn["ws"] = true
	doc, _, _, code = h.patch("prod", "GHSA-96hv-2xvq-fx4p", "--verify")
	if code != exitWrong || doc.Findings[0].Status != vulnpatch.StatusStillPresent {
		t.Errorf("still present: exit %d %+v", code, doc.Findings)
	}
}

func TestVulnPatchNamedAdvisories(t *testing.T) {
	h := newPatchHarness(t)
	doc, _, _, code := h.patch("prod", "GHSA-96hv-2xvq-fx4p", "GHSA-nope")
	if code != exitUndetermined {
		t.Errorf("exit %d, want 2 (a named advisory matched nothing)", code)
	}
	if r := doc.Findings[1]; r.ID != "GHSA-nope" || r.Status != vulnpatch.StatusSkipped || !strings.Contains(r.Reason, "prod release v1.0.0") {
		t.Errorf("missing advisory = %+v", r)
	}
	// A named finding outside every patcher (and with no fix) is reported,
	// not dropped: the env-wide default leaves it out.
	h = newPatchHarness(t)
	doc, _, _, code = h.patch("prod", "CVE-2026-82560")
	if code != exitUndetermined || len(doc.Findings) != 1 || !strings.Contains(doc.Findings[0].Reason, "OS package (deb)") {
		t.Errorf("unfixable: exit %d %+v", code, doc.Findings)
	}
}

func TestVulnPatchPR(t *testing.T) {
	h := newPatchHarness(t)
	h.dirty = true
	doc, _, stderr, code := h.patch("prod", "--pr", "--ecosystem", "npm")
	if code != exitRefused || doc.Changed || len(doc.Refusals) != 1 || !strings.Contains(stderr, "uncommitted") {
		t.Fatalf("dirty tree: exit %d changed %v refusals %v: %s", code, doc.Changed, doc.Refusals, stderr)
	}
	if !reflect.DeepEqual(h.cmds, []string{".: git status --porcelain"}) {
		t.Errorf("a refused patch ran %v", h.cmds)
	}

	h = newPatchHarness(t)
	doc, _, stderr, code = h.patch("prod", "--pr", "--ecosystem", "npm", "--verify")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	branch := "forge/vuln-patch/20261120-abc1234"
	if doc.PR == nil || doc.PR.Branch != branch || !doc.PR.Committed || doc.PR.URL != "https://github.com/acme/shop/pull/7" {
		t.Errorf("pr = %+v", doc.PR)
	}
	want := []string{
		".: git status --porcelain",
		"web: npm install", "web: npm install react-router@7.18.0",
		".: sh -c task test:short",
		".: git rev-parse --short HEAD",
		".: git switch -c " + branch,
		".: git add -A -- .",
	}
	if len(h.cmds) < len(want)+3 || !reflect.DeepEqual(h.cmds[:len(want)], want) {
		t.Fatalf("commands = %v", h.cmds)
	}
	if !strings.HasPrefix(h.cmds[len(want)], ".: git commit -m fix(deps): bump 2 vulnerable dependencies in prod") ||
		h.cmds[len(want)+1] != ".: git push -u origin "+branch || h.cmds[len(want)+2] != ".: gh pr create --head "+branch {
		t.Errorf("commit/push/pr = %v", h.cmds[len(want):])
	}

	// A patch that does not verify opens no pull request.
	h = newPatchHarness(t)
	h.failSh = true
	doc, _, _, code = h.patch("prod", "--pr", "--ecosystem", "npm", "--verify", "--branch", "sec/fix")
	if code != exitWrong || doc.PR == nil || doc.PR.Committed || doc.PR.URL != "" {
		t.Errorf("red patch: exit %d pr %+v", code, doc.PR)
	}
	for _, c := range h.cmds {
		if strings.Contains(c, "git commit") || strings.Contains(c, "gh pr") {
			t.Errorf("a red patch ran %q", c)
		}
	}
}

func TestVulnPatchFlagsAndInputs(t *testing.T) {
	h := newPatchHarness(t)
	for _, args := range [][]string{
		{"prod", "--pr", "--no-pr"},
		{"prod", "--branch", "x"},
		{"prod", "--ecosystem", "cargo"},
	} {
		if _, _, stderr, code := h.patch(args...); code != 1 || stderr == "" {
			t.Errorf("%v: exit %d %q", args, code, stderr)
		}
	}
	if len(h.cmds) != 0 {
		t.Errorf("a refused flag set ran %v", h.cmds)
	}

	// An env that records on a control plane is F8's: say so, exit 2.
	h = newPatchHarness(t)
	h.env.Endpoint = "https://cp.example"
	if _, _, stderr, code := h.patch("prod"); code != exitUndetermined || !strings.Contains(stderr, "control plane") {
		t.Errorf("hosted env: exit %d %s", code, stderr)
	}
}

func TestVulnPatchDirectory(t *testing.T) {
	h := newPatchHarness(t)
	web := filepath.Join(h.dir, "web")
	h.stillIn = map[string]bool{"ws": true, "react-router": true, "zlib": true}
	doc, _, stderr, code := h.patch(web, "--ecosystem", "npm")
	if code != 0 || doc.Env != "" || doc.Dir != web || len(doc.Findings) != 2 {
		t.Fatalf("exit %d doc %+v: %s", code, doc, stderr)
	}
	if len(h.installs) == 0 {
		t.Error("a directory patch did not install the scanner")
	}
	if want := []string{"web: npm install", "web: npm install react-router@7.18.0"}; !reflect.DeepEqual(h.cmds, want) {
		t.Errorf("commands = %v, want %v", h.cmds, want)
	}
}

// TestVulnPatchSchemaIsCurrent regenerates the vuln-patch schema from
// vulnpatch.Document and fails when the committed file differs.
func TestVulnPatchSchemaIsCurrent(t *testing.T) {
	got, err := generateVulnPatchSchema(newSourceDescriber(t, "../.."))
	if err != nil {
		t.Fatal(err)
	}
	const path = "../../schema/vuln-patch.v1.schema.json"
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("schema/vuln-patch.v1.schema.json is stale: vulnpatch.Document changed.\n" +
			"Regenerate it: FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run TestVulnPatchSchemaIsCurrent")
	}
	if !bytes.Equal(want, vulnPatchSchemaJSON()) {
		t.Errorf("the embedded schema (schema.VulnPatchV1) is not the committed file")
	}
	var out bytes.Buffer
	if err := runProjectSchema(&out, "vuln-patch"); err != nil || !bytes.Equal(out.Bytes(), vulnPatchSchemaJSON()) {
		t.Fatalf("forge project schema vuln-patch: %v", err)
	}
}

// The vulnpatch golden document validates against the published schema.
func TestVulnPatchSchemaValidatesTheGoldenDocument(t *testing.T) {
	t.Parallel()
	golden, err := os.ReadFile("../vulnpatch/testdata/vuln-patch.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	decodePatchDoc(t, golden)
}
