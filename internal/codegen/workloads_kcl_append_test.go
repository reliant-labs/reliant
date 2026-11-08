package codegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kcltest"
)

// workloadsPrefix is everything above the ALL list in the fixtures: real,
// schema-valid declarations for every identifier any case's list names, so
// the full-mode test can evaluate each result against forge's KCL module.
const workloadsPrefix = `import forge
import forge.workloads as fw

migrate = fw.Workload {name = "migrate", kind = "job", image = "example.com/demo", args = ["db", "migrate", "up"]}
billing = fw.Workload {name = "billing", image = "example.com/demo"}
admin_server = fw.Workload {name = "admin-server", image = "example.com/demo"}
admin_api = fw.Workload {name = "admin-api", image = "example.com/demo"}
idp_provision_oneshot = fw.Workload {name = "idp-provision-oneshot", kind = "tool", image = "example.com/demo"}

`

// allListComment documents the list, the way both the scaffolded template
// and control-plane's hand-edited workloads.k do. The declaration is inserted
// ABOVE it, so it must survive untouched.
const allListComment = "# Every workload this project declares.\n"

var overviewAdmin = config.ComponentConfig{Name: "overview-admin", Kind: config.ComponentKindServer}

// appendCase is one shape of the ALL list as a user may have left it, and the
// list as it must read after `forge scaffold service overview-admin`.
type appendCase struct {
	name      string
	list      string
	want      string
	wantNames []string
}

// allListShapes covers every shape a hand-owned ALL list takes. The first is
// control-plane's real one (multi-line, trailing comma), the shape that came
// out of `forge scaffold service overview_admin` as
// `idp_provision_oneshot,, overview_admin]` — a double comma, and the whole
// list reflowed onto the first line's indentation.
var allListShapes = []appendCase{
	{
		name:      "multi-line, trailing comma (control-plane)",
		list:      "ALL: [fw.Workload] = [\n    admin_server,\n    admin_api,\n    idp_provision_oneshot,\n]\n",
		want:      "ALL: [fw.Workload] = [\n    admin_server,\n    admin_api,\n    idp_provision_oneshot,\n    overview_admin,\n]\n",
		wantNames: []string{"admin-server", "admin-api", "idp-provision-oneshot", "overview-admin"},
	},
	{
		name:      "single line (the scaffolded template)",
		list:      "ALL: [fw.Workload] = [migrate, billing]\n",
		want:      "ALL: [fw.Workload] = [migrate, billing, overview_admin]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		name:      "single line, trailing comma",
		list:      "ALL: [fw.Workload] = [migrate, billing,]\n",
		want:      "ALL: [fw.Workload] = [migrate, billing, overview_admin,]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		name:      "empty",
		list:      "ALL: [fw.Workload] = []\n",
		want:      "ALL: [fw.Workload] = [overview_admin]\n",
		wantNames: []string{"overview-admin"},
	},
	{
		name:      "multi-line, no trailing comma",
		list:      "ALL: [fw.Workload] = [\n    migrate,\n    billing\n]\n",
		want:      "ALL: [fw.Workload] = [\n    migrate,\n    billing,\n    overview_admin\n]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		name:      "multi-line, newline-separated (no commas at all)",
		list:      "ALL: [fw.Workload] = [\n    migrate\n    billing\n]\n",
		want:      "ALL: [fw.Workload] = [\n    migrate\n    billing\n    overview_admin\n]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		// Comments between entries, a trailing comment on the last entry,
		// brackets and commas INSIDE comments, and a commented-out entry
		// after the last real one. None of it may be mistaken for list
		// syntax, and the new entry lands beside its peers, above the
		// commented-out one.
		name: "comments everywhere",
		list: "ALL: [fw.Workload] = [  # see [ordering], below\n" +
			"    migrate,  # runs first\n" +
			"    # the API tier ]\n" +
			"    billing  # public, ]\n" +
			"    # retired: legacy,\n" +
			"]\n",
		want: "ALL: [fw.Workload] = [  # see [ordering], below\n" +
			"    migrate,  # runs first\n" +
			"    # the API tier ]\n" +
			"    billing,  # public, ]\n" +
			"    overview_admin\n" +
			"    # retired: legacy,\n" +
			"]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		// Elements on the opening line, bracket below. KCL lists are
		// indentation-sensitive: an indented line after `[migrate, billing,`
		// is a syntax error, so the entry must join the opening line.
		name:      "entries on the opening line, bracket below",
		list:      "ALL: [fw.Workload] = [migrate, billing,\n]\n",
		want:      "ALL: [fw.Workload] = [migrate, billing, overview_admin,\n]\n",
		wantNames: []string{"migrate", "billing", "overview-admin"},
	},
	{
		name:      "multi-line, nothing but a comment",
		list:      "ALL: [fw.Workload] = [\n    # nothing deployed yet\n]\n",
		want:      "ALL: [fw.Workload] = [\n    # nothing deployed yet\n    overview_admin,\n]\n",
		wantNames: []string{"overview-admin"},
	},
}

// appendInto writes content as a project's workloads.k, runs the scaffold's
// append for comp, and returns the result.
func appendInto(t *testing.T, content string, comp config.ComponentConfig) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, WorkloadsKCLRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := AppendWorkloadStanza(dir, "github.com/acme/demo", "demo", comp)
	if err != nil {
		t.Fatalf("AppendWorkloadStanza: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), applied
}

// TestAppendWorkloadStanza_ListShapes pins the WHOLE file after an append, for
// every list shape: the declaration lands above the list's comment block, the
// list gains exactly one entry in its own style, and nothing else moves.
// Byte-exact on purpose — "valid KCL" alone would still admit an edit that
// reflows a user's list, which is the other half of the defect.
func TestAppendWorkloadStanza_ListShapes(t *testing.T) {
	stanza := WorkloadStanza("github.com/acme/demo", "demo", overviewAdmin)
	for _, tc := range allListShapes {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := appendInto(t, workloadsPrefix+allListComment+tc.list, overviewAdmin)
			if !applied {
				t.Fatalf("applied = false; want the declaration written")
			}
			want := workloadsPrefix + stanza + "\n" + allListComment + tc.want
			if got != want {
				t.Errorf("workloads.k after append:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestAppendWorkloadStanza_Idempotent: a second scaffold of the same workload
// (--resume, --force, a re-run) writes nothing — no second declaration, which
// KCL would silently let shadow the first, and no second ALL entry.
func TestAppendWorkloadStanza_Idempotent(t *testing.T) {
	first, applied := appendInto(t, workloadsPrefix+allListComment+allListShapes[0].list, overviewAdmin)
	if !applied {
		t.Fatal("first append not applied")
	}
	second, applied := appendInto(t, first, overviewAdmin)
	if applied {
		t.Error("second append reported applied; want a no-op")
	}
	if second != first {
		t.Errorf("second append changed the file:\n%s\nwas:\n%s", second, first)
	}
}

// TestAppendWorkloadStanza_AlreadyListed: a project that names the workload in
// ALL but has no declaration (deleted by hand, or listed ahead of time) gets
// the declaration and NO second list entry — a duplicate entry would deploy
// the same workload twice.
func TestAppendWorkloadStanza_AlreadyListed(t *testing.T) {
	list := "ALL: [fw.Workload] = [\n    migrate,\n    overview_admin,  # declared below by scaffold\n]\n"
	got, applied := appendInto(t, workloadsPrefix+allListComment+list, overviewAdmin)
	if !applied {
		t.Fatal("applied = false; want the missing declaration written")
	}
	want := workloadsPrefix + WorkloadStanza("github.com/acme/demo", "demo", overviewAdmin) + "\n" + allListComment + list
	if got != want {
		t.Errorf("workloads.k:\n%s\nwant:\n%s", got, want)
	}
}

// TestAppendWorkloadStanza_AmbiguousListIsNotEdited: with two ALL lists, or
// one whose closing bracket is missing, there is no unambiguous edit — the
// file is left alone and the caller prints the stanza.
func TestAppendWorkloadStanza_AmbiguousListIsNotEdited(t *testing.T) {
	for name, content := range map[string]string{
		"two lists":    workloadsPrefix + "ALL: [fw.Workload] = [migrate]\nALL: [fw.Workload] = [billing]\n",
		"unterminated": workloadsPrefix + "ALL: [fw.Workload] = [\n    migrate,\n",
		"no list":      workloadsPrefix,
	} {
		t.Run(name, func(t *testing.T) {
			got, applied := appendInto(t, content, overviewAdmin)
			if applied || got != content {
				t.Errorf("applied=%v, file changed=%v; want neither", applied, got != content)
			}
		})
	}
}

// TestAppendWorkloadStanza_ResultEvaluates runs every appended file through
// the real kcl against forge's own KCL module and checks what ALL evaluates
// to. The byte-exact test above proves the edit is minimal; this one proves
// the result is a valid KCL program that deploys exactly the intended
// workloads, in order.
func TestAppendWorkloadStanza_ResultEvaluates(t *testing.T) {
	if testing.Short() {
		t.Skip("runs kcl; full mode only")
	}
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH")
	}
	for _, tc := range allListShapes {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := appendInto(t, workloadsPrefix+allListComment+tc.list, overviewAdmin)
			dir := t.TempDir()
			mod := "[package]\nname = \"appendcheck\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\nforge = { path = \"" + forgeKCLModuleDir(t) + "\" }\n"
			for f, c := range map[string]string{"kcl.mod": mod, "main.k": got} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, err := kcltest.Run(t.Context(), dir, "run", "main.k", "--format", "json")
			if err != nil {
				t.Fatalf("kcl rejected the appended workloads.k: %v\n%s\n--- file ---\n%s", err, out, got)
			}
			var doc struct {
				ALL []struct {
					Name string `json:"name"`
				} `json:"ALL"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("decode kcl output: %v\n%s", err, out)
			}
			names := make([]string, 0, len(doc.ALL))
			for _, w := range doc.ALL {
				names = append(names, w.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Errorf("ALL evaluates to %v, want %v", names, tc.wantNames)
			}
		})
	}
}

// `forge scaffold operator` declares its workload with `crds = []` — it owns
// no CRD yet — and `forge scaffold crd` adds each one. The operator's derived
// ClusterRole covers exactly what `crds` lists, so each new kind lands there,
// written in the style the list already has, and nothing else in the file
// moves.
func TestAppendOperatorCRD(t *testing.T) {
	const docstring = `"""
What demo RUNS. A worked example, NOT a declaration:

    reaper = fw.Workload {
        name = "reaper"
        crds = ["Example"]
    }
"""
`
	const opFresh = `reaper = fw.Workload {
    name = "reaper"
    kind = "operator"
    group = "demo.io"
    crds = []
    config_secrets = ["DATABASE_URL"]
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, WorkloadsKCLRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if edit, err := AppendOperatorCRD(dir, "reaper", "Widget"); err != nil || edit != CRDListNotEditable {
		t.Fatalf("no workloads.k: %v, %v — want not editable", edit, err)
	}
	other := "billing = fw.Workload {name = \"billing\", image = \"example.com/demo\"}\n"
	if err := os.WriteFile(path, []byte(docstring+other+opFresh), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	for _, kind := range []string{"Widget", "Gadget"} {
		if edit, err := AppendOperatorCRD(dir, "reaper", kind); err != nil || edit != CRDListAdded {
			t.Fatalf("append %s: %v, %v", kind, edit, err)
		}
	}
	want := docstring + other + strings.Replace(opFresh, "crds = []", `crds = ["Widget", "Gadget"]`, 1)
	if got := read(); got != want {
		t.Errorf("after two CRDs:\n%s\nwant:\n%s", got, want)
	}

	if edit, err := AppendOperatorCRD(dir, "reaper", "Widget"); err != nil || edit != CRDListAlreadyListed {
		t.Errorf("re-adding Widget: %v, %v — want already listed", edit, err)
	}
	if got := read(); got != want {
		t.Errorf("an already-listed CRD rewrote the file:\n%s", got)
	}
	// A workload with no `crds` field, and one that does not exist: forge
	// does not guess where the field goes.
	for _, name := range []string{"billing", "nope"} {
		if edit, err := AppendOperatorCRD(dir, name, "Widget"); err != nil || edit != CRDListNotEditable {
			t.Errorf("%s: %v, %v — want not editable", name, edit, err)
		}
	}
	if got := read(); got != want {
		t.Errorf("a refused edit wrote the file:\n%s", got)
	}
}
