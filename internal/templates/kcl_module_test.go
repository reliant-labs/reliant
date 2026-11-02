package templates

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// The kcl/tests fixture corpus.
//
// Every fixture is rendered THE WAY FORGE RENDERS A PROJECT: through
// kclrender.Run, which supplies `import forge` from the module embedded in
// this binary (internal/kclvendor, #277) and registers kcl_plugin.forge.
// There is no `kcl` binary on the path and no kcl.mod dependency, so a
// fixture exercises exactly the module a released forge would render with.
//
// A fixture states how it is run with header directives (a comment line
// anywhere in the file, `# <directive>: <value>`):
//
//	# kcl-args: env=dev              -D bindings for the render. Several
//	# kcl-args: env=dev | env=e2e    `|`-separated sets run once each.
//	                                 Bindings are whitespace-separated, so a
//	                                 value may not contain a space or `|`;
//	                                 a string is a KCL literal, quoted the
//	                                 way forge quotes it (image_tag="3826648").
//	# reject-args: env=prod          also render under these bindings and
//	# reject-expect: <substring>     require a refusal naming <substring>.
//	# expect: <substring>            (negative_* / closedschema_*) the
//	                                 refusal must contain it. Repeatable;
//	                                 REQUIRED on every negative fixture.
//
// Kinds, by file name:
//
//	positive*.k      must evaluate, declare at least one `assert_*`, and
//	                 every `assert_*` must be true.
//	negative_*.k     must be REFUSED BY FORGE — a schema `check:` or a
//	                 render `assert` — with every `# expect:` substring in
//	                 the refusal message. A type error, a compile error or a
//	                 missing attribute does not count, however it is worded.
//	closedschema_*.k must fail to compile, naming every `# expect:`
//	                 substring (an undeclared member of a closed schema is a
//	                 compile-time error, which negative_* deliberately
//	                 refuses to accept).
//
// WHY `# expect:` IS MANDATORY. "It failed" is not evidence that the rule
// under test fired. During a schema migration a renamed field or a deleted
// helper makes every negative fixture fail — with "attribute not found" —
// so a harness that only checks for failure reports the whole corpus green
// while validating nothing. The substring ties each fixture to the one
// message it exists to pin.

var (
	kclCacheOnce sync.Once
	kclCacheDir  string
)

// kclTestModuleCache points kclvendor at a per-process temp directory, once,
// before any parallel subtest renders, so these tests never write into the
// developer's real <UserCacheDir>/forge/kcl.
func kclTestModuleCache(t *testing.T) {
	t.Helper()
	kclCacheOnce.Do(func() {
		dir, err := os.MkdirTemp("", "forge-kcl-module-test-")
		if err != nil {
			panic(err)
		}
		kclCacheDir = dir
		kclvendor.SetCacheDirForTest(dir)
		// Removed by TestMain, not t.Cleanup: the directory is shared across
		// every subtest via this sync.Once, so only the process can own its
		// lifetime. See main_test.go.
		RegisterSharedTempDir(dir)
	})
}

// kclModuleRoot resolves the absolute path to the kcl/ module directory at
// the repo root, after reading every file under it as a test input.
func kclModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	root := wd
	for range []int{1, 2, 3} {
		if _, err := os.Stat(filepath.Join(root, "kcl", "kcl.mod")); err == nil {
			dir := filepath.Join(root, "kcl")
			readInputTree(t, dir)
			return dir
		}
		root = filepath.Dir(root)
	}
	t.Fatalf("could not locate kcl/ module root from cwd %s", wd)
	return ""
}

// readInputTree reads every file under dir from THIS process, so go test's
// cache keys the result on them.
//
// The cache records only what the test process itself opens or stats. A
// render reads its sources natively, outside Go's os package, so a file only
// the render touches — kcl/example is not go:embed'd — is invisible to it,
// and a cached ok survives an edit that breaks the render. Reading the tree
// here is what makes those files inputs.
func readInputTree(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		_, err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatalf("read %s as test input: %v", dir, err)
	}
}

// requireKCLRender registers the kcl_plugin.forge namespace for this test
// binary before a render.
func requireKCLRender(t *testing.T) {
	t.Helper()
	kclplugin.Register()
	kclTestModuleCache(t)
}

// runKCL renders one .k file through forge's own KCL seam (kclrender.Run) with
// the given `-D` bindings, from the file's directory. The returned document is
// the render's JSON. The render runs in a pooled worker process, because one
// process can only evaluate KCL one render at a time: see kcl_render_workers_test.go.
func runKCL(t *testing.T, entry string, dArgs ...string) ([]byte, error) {
	t.Helper()
	return runKCLInWorker(t, filepath.Dir(entry), entry, dArgs)
}

// ansiRE strips the colour escapes kcl writes into its diagnostics.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// kclDiagnostics is the refusal text a failed render reports, with colour
// stripped and with the ECHOED SOURCE LINES removed. kcl quotes the offending
// source line (`12 | _bad = forge.X {...}`) above each message; matching an
// expect substring against that echo would let a fixture satisfy its own
// assertion by containing the words in a string literal. Only the message
// lines (`|  <message>` / `| ^ <message>`) and error headers are kept.
func kclDiagnostics(err error) string {
	if err == nil {
		return ""
	}
	var keep []string
	sc := bufio.NewScanner(strings.NewReader(ansiRE.ReplaceAllString(err.Error(), "")))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sourceLine := regexp.MustCompile(`^\s*\d+\s*\|`)
	for sc.Scan() {
		line := sc.Text()
		if sourceLine.MatchString(line) {
			continue
		}
		keep = append(keep, strings.TrimSpace(line))
	}
	return strings.Join(keep, "\n")
}

// A forge refusal is a schema check or a render assert. Everything else —
// a type error, a missing attribute, a compile error — is a broken fixture
// or a broken module, and must not pass as "the rule fired".
var (
	forgeRefusalRE = regexp.MustCompile(`Check failed on the condition|EvaluationError`)
	notARefusalREs = []*regexp.Regexp{
		regexp.MustCompile(`TypeError`),
		regexp.MustCompile(`CompileError`),
		regexp.MustCompile(`CannotFindModule`),
		regexp.MustCompile(`attribute '[^']*' not found`),
		regexp.MustCompile(`attribute '[^']*' of \S+ is required`),
		regexp.MustCompile(`has no attribute`),
		regexp.MustCompile(`name '[^']*' is not defined`),
	}
)

// kclDirectives is a fixture's header: `# name: value` comment lines.
type kclDirectives map[string][]string

func readDirectives(t *testing.T, path string) kclDirectives {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	d := kclDirectives{}
	re := regexp.MustCompile(`^\s*#\s*(kcl-args|reject-args|reject-expect|expect):\s*(.+?)\s*$`)
	for _, line := range strings.Split(string(b), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			d[m[1]] = append(d[m[1]], m[2])
		}
	}
	return d
}

// argSets splits a `kcl-args` value into its run sets: `a=1 b=2 | a=3` is
// two renders. No directive is one render with no bindings.
func argSets(values []string) [][]string {
	if len(values) == 0 {
		return [][]string{nil}
	}
	var sets [][]string
	for _, v := range values {
		for _, set := range strings.Split(v, "|") {
			sets = append(sets, strings.Fields(set))
		}
	}
	return sets
}

// fixtures lists kcl/tests/<prefix>*.k, sorted.
func fixtures(t *testing.T, prefix string) (string, []string) {
	t.Helper()
	dir := filepath.Join(kclModuleRoot(t), "tests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tests dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".k") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("no %s*.k fixtures in %s", prefix, dir)
	}
	return dir, names
}

// assertAllTrue requires at least one assert_* and every one true.
func assertAllTrue(t *testing.T, label string, out []byte) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%s: unmarshal render JSON: %v\n%s", label, err, out)
	}
	var n int
	for k, v := range parsed {
		if !strings.HasPrefix(k, "assert_") {
			continue
		}
		n++
		if b, ok := v.(bool); !ok {
			t.Errorf("%s: %q is not a bool: %v", label, k, v)
		} else if !b {
			t.Errorf("%s: assertion %q is false", label, k)
		}
	}
	if n == 0 {
		t.Errorf("%s: declares no assert_* identifiers — a fixture that asserts nothing passes vacuously", label)
	}
}

// refusalProblem is "" when err is a forge refusal (a schema check or a
// render assert — not a type, compile or missing-attribute error) whose
// message contains every expected substring, and otherwise says why not.
func refusalProblem(err error, out []byte, expects []string) string {
	if err == nil {
		return "expected forge to refuse this render, but it succeeded:\n" + string(out)
	}
	diag := kclDiagnostics(err)
	for _, re := range notARefusalREs {
		if re.MatchString(diag) {
			return "failed, but not by a forge rule (" + re.String() + ") — the fixture or the module is broken, so the rule under test was never evaluated:\n" + diag
		}
	}
	if !forgeRefusalRE.MatchString(diag) {
		return "failed without a schema check or render assert:\n" + diag
	}
	var missing []string
	for _, want := range expects {
		if !strings.Contains(diag, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return "refusal does not name " + strings.Join(missing, ", ") + ":\n" + diag
	}
	return ""
}

func assertRefusal(t *testing.T, label string, err error, out []byte, expects []string) {
	t.Helper()
	if p := refusalProblem(err, out, expects); p != "" {
		t.Errorf("%s: %s", label, p)
	}
}

// TestKCLModule_PositiveAssertions: every positive*.k renders under each of
// its `# kcl-args` sets with every assert_* true, and — when it declares
// `# reject-args` — is refused under those.
func TestKCLModule_PositiveAssertions(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates every KCL module fixture; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)
	dir, names := fixtures(t, "positive")
	for _, name := range names {
		path := filepath.Join(dir, name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := readDirectives(t, path)
			for _, args := range argSets(d["kcl-args"]) {
				label := name + " " + strings.Join(args, " ")
				out, err := runKCL(t, path, args...)
				if err != nil {
					t.Errorf("%s: render failed: %s", label, kclDiagnostics(err))
					continue
				}
				assertAllTrue(t, label, out)
			}
			if rej := d["reject-args"]; len(rej) > 0 {
				if len(d["reject-expect"]) == 0 {
					t.Fatalf("%s declares reject-args without reject-expect", name)
				}
				for _, args := range argSets(rej) {
					out, err := runKCL(t, path, args...)
					assertRefusal(t, name+" "+strings.Join(args, " "), err, out, d["reject-expect"])
				}
			}
		})
	}
}

// TestKCLModule_NegativeChecks: every negative_*.k is refused by a forge
// rule whose message names every `# expect:` substring.
func TestKCLModule_NegativeChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates every KCL module fixture; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)
	dir, names := fixtures(t, "negative_")
	for _, name := range names {
		path := filepath.Join(dir, name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := readDirectives(t, path)
			if len(d["expect"]) == 0 {
				t.Fatalf("%s has no `# expect: <substring>` directive: a negative fixture must name the refusal it pins", name)
			}
			for _, args := range argSets(d["kcl-args"]) {
				out, err := runKCL(t, path, args...)
				assertRefusal(t, name, err, out, d["expect"])
			}
		})
	}
}

// TestKCLModule_ClosedSchemas: a closedschema_*.k declares a member its
// schema does not have; KCL refuses it at compile time, and the message must
// name the member and the schema. This is how a hosted-facing schema refuses
// configuration it must not honour — by not declaring it.
func TestKCLModule_ClosedSchemas(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates every KCL module fixture; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)
	dir, names := fixtures(t, "closedschema_")
	for _, name := range names {
		path := filepath.Join(dir, name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := readDirectives(t, path)
			if len(d["expect"]) == 0 {
				t.Fatalf("%s has no `# expect:` directive naming the refused member and schema", name)
			}
			out, err := runKCL(t, path)
			if err == nil {
				t.Fatalf("expected the closed schema to refuse %s, but it rendered:\n%s", name, out)
			}
			diag := kclDiagnostics(err)
			for _, want := range d["expect"] {
				if !strings.Contains(diag, want) {
					t.Errorf("refusal should name %q:\n%s", want, diag)
				}
			}
		})
	}
}

// TestKCLModule_HarnessRefusesVacuousNegatives pins the harness itself: a
// negative fixture that fails for the WRONG reason (a missing attribute, a
// type error) or whose message lacks its expect substring must not pass,
// and an expect substring that appears only in the ECHOED SOURCE must not
// satisfy it.
func TestKCLModule_HarnessRefusesVacuousNegatives(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("kcl.mod", "[package]\nname = \"harness_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")

	cases := []struct {
		name, body string
		expect     []string
		wantPass   bool
	}{
		{"real refusal", "schema A:\n    x: int\n    check:\n        x > 1, \"A.x must exceed one\"\na = A {x = 0}\n", []string{"A.x must exceed one"}, true},
		{"assert refusal", "assert 1 == 2, \"workload api: sidecars not allowed on OnHosted\"\n", []string{"sidecars not allowed"}, true},
		{"wrong message", "schema A:\n    x: int\n    check:\n        x > 1, \"A.x must exceed one\"\na = A {x = 0}\n", []string{"replicas"}, false},
		{"missing attribute", "schema A:\n    x: int\na = A {}\n", []string{"x"}, false},
		{"unknown attribute", "import forge\na = forge.NoSuchSchema {}\n", []string{"NoSuchSchema"}, false},
		{"expect only in echoed source", "_msg = \"sidecars not allowed\"\nschema A:\n    x: int\n    check:\n        x > 1, \"A.x must exceed one\"\na = A {x = 0}\n", []string{"sidecars not allowed"}, false},
	}
	for _, c := range cases {
		p := write(strings.ReplaceAll(c.name, " ", "_")+".k", c.body)
		out, err := runKCL(t, p)
		problem := refusalProblem(err, out, c.expect)
		if passed := problem == ""; passed != c.wantPass {
			t.Errorf("%s: harness verdict pass=%v, want %v (%s)", c.name, passed, c.wantPass, problem)
		}
	}
}

// renderContractDir is where the §9.1 contract goldens live: one
// `<case>.json` per runtime shape — the WHOLE document kclrender.Run emits
// (`{"output": …}`) — next to the `<case>.k` source that produces it. P2b
// decodes the same JSON (internal/cli kcl_render_test.go); this test is the
// KCL leg of that triple: the render must REPRODUCE each golden.
func renderContractDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(kclModuleRoot(t)), "internal", "cli", "testdata", "render_contract")
	readInputTree(t, dir)
	return dir
}

// renderContractUpdateEnv regenerates the goldens from the fixtures instead
// of comparing: FORGE_UPDATE_GOLDEN=1 go test ./internal/templates -run
// TestKCLModule_RenderContract. Review the diff as a contract change — a
// changed key is one P2b's decoder must follow.
const renderContractUpdateEnv = "FORGE_UPDATE_GOLDEN"

// TestKCLModule_RenderContract renders each render_contract/<case>.k and
// requires the rendered document to equal <case>.json.
func TestKCLModule_RenderContract(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates every KCL module fixture; runs in task test")
	}
	t.Parallel()
	requireKCLRender(t)
	dir := renderContractDir(t)
	srcs, _ := filepath.Glob(filepath.Join(dir, "*.k"))
	if len(srcs) == 0 {
		t.Fatalf("no render-contract fixtures (*.k) in %s", dir)
	}
	update := os.Getenv(renderContractUpdateEnv) == "1"
	sort.Strings(srcs)
	for _, src := range srcs {
		name := strings.TrimSuffix(filepath.Base(src), ".k")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			args := argSets(readDirectives(t, src)["kcl-args"])[0]
			out, err := runKCL(t, src, args...)
			if err != nil {
				t.Fatalf("render %s: %s", name, kclDiagnostics(err))
			}
			var doc map[string]any
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("unmarshal render: %v", err)
			}
			if _, ok := doc["output"]; !ok {
				t.Fatalf("%s.k renders no top-level `output` (it must end with `output = forge.render(bundle)`)", name)
			}
			gb, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			gb = append(gb, '\n')
			goldenPath := filepath.Join(dir, name+".json")
			if update {
				if err := os.WriteFile(goldenPath, gb, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			golden, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("golden %s.json missing beside %s.k (regenerate with %s=1): %v", name, name, renderContractUpdateEnv, err)
			}
			// Compare canonically (keys sorted by MarshalIndent of a
			// map), so the golden's own formatting is not the contract.
			var want any
			if err := json.Unmarshal(golden, &want); err != nil {
				t.Fatalf("unmarshal golden: %v", err)
			}
			wb, _ := json.MarshalIndent(want, "", "  ")
			wb = append(wb, '\n')
			if string(gb) != string(wb) {
				t.Errorf("forge.render drifted from the contract golden %s.json (regenerate with %s=1 and review the diff)\n%s",
					name, renderContractUpdateEnv, firstDiff(string(gb), string(wb)))
			}
		})
	}
}

// firstDiff reports the first differing line of two renders with context,
// so a drift names the key instead of dumping two whole documents.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			lo := max(0, i-3)
			ctx := strings.Join(w[lo:min(i, len(w))], "\n")
			return fmt.Sprintf("line %d:\n%s\n--- render:  %s\n+++ golden:  %s", i+1, ctx, gl, wl)
		}
	}
	return "(no line differs)"
}

// TestKCLModule_ExampleRendersOneOutput pins the single entrypoint on the
// module's own example env: it renders `output` and no top-level
// `manifests` (a second, bypass-able applyable stream).
func TestKCLModule_ExampleRendersOneOutput(t *testing.T) {
	t.Parallel()
	requireKCLRender(t)
	entry := filepath.Join(kclModuleRoot(t), "example", "dev", "main.k")
	out, err := runKCL(t, entry)
	if err != nil {
		t.Fatalf("render example/dev: %s", kclDiagnostics(err))
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	output, ok := doc["output"].(map[string]any)
	if !ok {
		t.Fatalf("example/dev renders no `output` object")
	}
	if _, ok := doc["manifests"]; ok {
		t.Error("example/dev renders a top-level `manifests`: the applyable stream is output.manifests only")
	}
	for _, key := range []string{"workloads", "manifests", "frontends", "config_maps", "gateways", "http_routes", "grpc_routes", "runtime_classes", "infra", "databases"} {
		if _, ok := output[key]; !ok {
			t.Errorf("output is missing the %q bucket", key)
		}
	}
	for _, gone := range []string{"services", "operators", "cronjobs", "jobs"} {
		if _, ok := output[gone]; ok {
			t.Errorf("output still carries the deleted %q bucket", gone)
		}
	}
	ws, _ := output["workloads"].([]any)
	for i, raw := range ws {
		w, _ := raw.(map[string]any)
		rt, _ := w["runtime"].(map[string]any)
		switch rt["type"] {
		case "host", "compose", "cluster", "hosted", "build-only":
		default:
			t.Errorf("workloads[%d].runtime.type = %v, want host|compose|cluster|hosted|build-only", i, rt["type"])
		}
	}
}
