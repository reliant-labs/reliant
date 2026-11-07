package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/obsdash"
)

const addPanelJSON = `{
  "id": "orders",
  "title": "Orders",
  "kind": "number",
  "signal": "traces",
  "series": [{"label": "n", "agg": "count"}]
}
`

const addPanelNoIDJSON = `{
  "title": "Order rate",
  "kind": "number",
  "signal": "traces",
  "series": [{"label": "n", "agg": "count"}]
}
`

type addPanelRun struct {
	out, errOut string
	err         error
}

func addPanel(t *testing.T, dir string, o obsDashboardsAddOpts, stdin string) addPanelRun {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runObsDashboardsAddPanel(dir, o, strings.NewReader(stdin), &out, &errOut)
	return addPanelRun{out.String(), errOut.String(), err}
}

func writePanelFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "panel.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func dashboardPath(dir, slug string) string {
	return filepath.Join(dir, filepath.FromSlash(obsdash.DashboardsDir), slug+".json")
}

func readDashboard(t *testing.T, dir, slug string) obsdash.Dashboard {
	t.Helper()
	p := dashboardPath(dir, slug)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	d, err := obsdash.ParseDashboard(p, b)
	if err != nil {
		t.Fatalf("written dashboard does not validate:\n%v", err)
	}
	return d
}

func panelIDs(d obsdash.Dashboard) []string {
	var ids []string
	for _, p := range d.Panels {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestObsDashboardsAddPanel_CreatesTheDashboard(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "shop-explore", panel: writePanelFile(t, addPanelJSON)}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	d := readDashboard(t, dir, "shop-explore")
	if d.ID != "shop-explore" || d.Title != "shop-explore" {
		t.Errorf("dashboard id/title = %q/%q, want the slug for both", d.ID, d.Title)
	}
	if got := panelIDs(d); len(got) != 1 || got[0] != "orders" {
		t.Errorf("panels = %v", got)
	}
	rel := obsdash.DashboardsDir + "/shop-explore.json"
	if !strings.Contains(r.out, rel) {
		t.Errorf("output does not name the path %s:\n%s", rel, r.out)
	}
	if !strings.Contains(r.out, "suggested: forge obs dashboards check") {
		t.Errorf("output lacks the suggested command:\n%s", r.out)
	}
	b, _ := os.ReadFile(dashboardPath(dir, "shop-explore"))
	if !strings.Contains(string(b), `"version": "forge.dashboards/v1"`) {
		t.Errorf("file lacks the spec version:\n%s", b)
	}
}

func TestObsDashboardsAddPanel_TitleFlag(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", title: "Operations", panel: writePanelFile(t, addPanelJSON)}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if d := readDashboard(t, dir, "ops"); d.Title != "Operations" {
		t.Errorf("title = %q", d.Title)
	}
}

func TestObsDashboardsAddPanel_AppendsAndGeneratesUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	first := writePanelFile(t, addPanelJSON)
	noID := writePanelFile(t, addPanelNoIDJSON)
	for _, p := range []string{first, noID, noID, noID} {
		if r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: p}, ""); r.err != nil {
			t.Fatal(r.err)
		}
	}
	got := panelIDs(readDashboard(t, dir, "ops"))
	want := []string{"orders", "order-rate", "order-rate-2", "order-rate-3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("panel ids = %v, want %v", got, want)
	}
}

func TestObsDashboardsAddPanel_IDFlagOverridesThePanelsOwn(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", id: "mine", panel: writePanelFile(t, addPanelJSON)}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := panelIDs(readDashboard(t, dir, "ops")); len(got) != 1 || got[0] != "mine" {
		t.Errorf("panel ids = %v, want [mine]", got)
	}
}

func TestObsDashboardsAddPanel_DuplicateIDFailsAndLeavesTheFileAlone(t *testing.T) {
	dir := t.TempDir()
	p := writePanelFile(t, addPanelJSON)
	if r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: p}, ""); r.err != nil {
		t.Fatal(r.err)
	}
	before, _ := os.ReadFile(dashboardPath(dir, "ops"))
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: p}, "")
	if r.err == nil {
		t.Fatal("a duplicate panel id must fail without --replace")
	}
	if !strings.Contains(r.err.Error(), `"orders"`) || !strings.Contains(r.err.Error(), "--replace") {
		t.Errorf("error should name the id and --replace: %v", r.err)
	}
	if exitcode.Of(r.err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(r.err))
	}
	if after, _ := os.ReadFile(dashboardPath(dir, "ops")); !bytes.Equal(before, after) {
		t.Error("a refused add changed the file")
	}
}

func TestObsDashboardsAddPanel_ReplaceKeepsPositionAndIsByteStable(t *testing.T) {
	dir := t.TempDir()
	if r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: writePanelFile(t, addPanelJSON)}, ""); r.err != nil {
		t.Fatal(r.err)
	}
	if r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: writePanelFile(t, addPanelNoIDJSON)}, ""); r.err != nil {
		t.Fatal(r.err)
	}
	updated := strings.Replace(addPanelJSON, `"title": "Orders"`, `"title": "Orders v2"`, 1)
	opts := obsDashboardsAddOpts{slug: "ops", replace: true, panel: writePanelFile(t, updated)}
	if r := addPanel(t, dir, opts, ""); r.err != nil {
		t.Fatal(r.err)
	}
	d := readDashboard(t, dir, "ops")
	if got := panelIDs(d); strings.Join(got, ",") != "orders,order-rate" {
		t.Fatalf("panel ids = %v, want the replaced panel to keep its place", got)
	}
	if d.Panels[0].Title != "Orders v2" {
		t.Errorf("title = %q, the panel was not replaced", d.Panels[0].Title)
	}
	first, _ := os.ReadFile(dashboardPath(dir, "ops"))
	if r := addPanel(t, dir, opts, ""); r.err != nil {
		t.Fatal(r.err)
	}
	second, _ := os.ReadFile(dashboardPath(dir, "ops"))
	if !bytes.Equal(first, second) {
		t.Errorf("a second identical run changed the file:\n%s\n---\n%s", first, second)
	}
}

func TestObsDashboardsAddPanel_ReplaceOnAFreshFileAdds(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", replace: true, panel: writePanelFile(t, addPanelJSON)}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := panelIDs(readDashboard(t, dir, "ops")); len(got) != 1 {
		t.Errorf("panel ids = %v", got)
	}
}

func TestObsDashboardsAddPanel_ReplaceNeedsAnID(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", replace: true, panel: writePanelFile(t, addPanelNoIDJSON)}, "")
	if r.err == nil || !strings.Contains(r.err.Error(), "--replace") {
		t.Fatalf("--replace with no id anywhere must fail naming --replace, got %v", r.err)
	}
}

func TestObsDashboardsAddPanel_InvalidPanelIsPositioned(t *testing.T) {
	dir := t.TempDir()
	bad := `{
  "id": "orders",
  "title": "Orders",
  "kind": "number",
  "signal": "traces",
  "series": [{"label": "n", "agg": "median"}]
}
`
	p := writePanelFile(t, bad)
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: p}, "")
	if r.err == nil {
		t.Fatal("an invalid panel must fail")
	}
	if exitcode.Of(r.err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(r.err))
	}
	// "median" is the value on line 6; the error carries the panel file's own position.
	want := p + ":6:"
	if !strings.Contains(r.err.Error(), want) {
		t.Errorf("error lacks a position in the panel file (%s):\n%v", want, r.err)
	}
	if _, err := os.Stat(dashboardPath(dir, "ops")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused add created the file")
	}
}

func TestObsDashboardsAddPanel_UnknownFieldIsRejected(t *testing.T) {
	dir := t.TempDir()
	typo := strings.Replace(addPanelJSON, `"signal"`, `"singal"`, 1)
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: writePanelFile(t, typo)}, "")
	if r.err == nil || !strings.Contains(r.err.Error(), "singal") {
		t.Fatalf("a misspelt field must be rejected by name, got %v", r.err)
	}
}

func TestObsDashboardsAddPanel_SyntaxErrorIsPositioned(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: "-"}, "{\n  \"id\": \"x\",\n  oops\n}\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "<stdin>:3:") {
		t.Fatalf("want a <stdin>:3: position, got %v", r.err)
	}
}

func TestObsDashboardsAddPanel_PanelFromStdin(t *testing.T) {
	dir := t.TempDir()
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: "-"}, addPanelJSON)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := panelIDs(readDashboard(t, dir, "ops")); len(got) != 1 || got[0] != "orders" {
		t.Errorf("panel ids = %v", got)
	}
}

func TestObsDashboardsAddPanel_SlugRule(t *testing.T) {
	p := writePanelFile(t, addPanelJSON)
	for slug, ok := range map[string]bool{
		"a": true, "shop-explore": true, "0abc": true, strings.Repeat("a", 63): true,
		"": false, "-a": false, "A": false, "a_b": false, "a.b": false, "../x": false,
		strings.Repeat("a", 64): false,
	} {
		dir := t.TempDir()
		r := addPanel(t, dir, obsDashboardsAddOpts{slug: slug, panel: p}, "")
		if (r.err == nil) != ok {
			t.Errorf("slug %q: err = %v, want ok=%v", slug, r.err, ok)
		}
	}
}

func TestObsDashboardsAddPanel_WarnsWhenTheSlugReplacesAGeneratedDashboard(t *testing.T) {
	dir := t.TempDir()
	in := obsdash.Input{
		Project:   "shop",
		Services:  []obsdash.Service{{Name: "orders", Package: "orders.v1", ProtoService: "OrdersService", Methods: []obsdash.Method{{Name: "GetOrder"}}}},
		Processes: []obsdash.Process{{Name: "shop-api", ServiceNames: []string{"shop-api"}, RPCServices: []string{"orders.v1.OrdersService"}}},
	}
	if _, err := obsdash.Write(dir, in, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	p := writePanelFile(t, addPanelJSON)

	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "shop-process-shop-api", panel: p}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(r.errOut, "replace") || !strings.Contains(r.errOut, "shop-process-shop-api") {
		t.Errorf("no replace warning on stderr:\n%s", r.errOut)
	}

	r = addPanel(t, dir, obsDashboardsAddOpts{slug: "mine", panel: p}, "")
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.errOut != "" {
		t.Errorf("unexpected warning for a fresh slug:\n%s", r.errOut)
	}
}

func TestObsDashboardsAddPanel_RefusesAHyperDXFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(dashboardPath(dir, "x")), 0o755); err != nil {
		t.Fatal(err)
	}
	hx := `{"name": "x", "tiles": []}`
	if err := os.WriteFile(dashboardPath(dir, "hdx"), []byte(hx), 0o644); err != nil {
		t.Fatal(err)
	}
	r := addPanel(t, dir, obsDashboardsAddOpts{slug: "hdx", panel: writePanelFile(t, addPanelJSON)}, "")
	if r.err == nil || !strings.Contains(r.err.Error(), "HyperDX") {
		t.Fatalf("want a HyperDX refusal, got %v", r.err)
	}
	if b, _ := os.ReadFile(dashboardPath(dir, "hdx")); string(b) != hx {
		t.Error("the HyperDX file was modified")
	}
}

func TestObsDashboardsAddPanel_ResultPassesCheck(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{addPanelJSON, addPanelNoIDJSON} {
		if r := addPanel(t, dir, obsDashboardsAddOpts{slug: "ops", panel: writePanelFile(t, p)}, ""); r.err != nil {
			t.Fatal(r.err)
		}
	}
	set, err := collectObservabilityFiles(dir, "")
	if err != nil {
		t.Fatalf("`forge obs dashboards check` rejects what add-panel wrote: %v", err)
	}
	var found bool
	for _, d := range set.Dashboards {
		if d.Name == "ops" && d.Source == obsdash.SourceUser && len(d.Dashboard.Panels) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("check does not see the dashboard: %+v", set.Dashboards)
	}
}

func TestObsDashboardsAddPanel_CommandWiring(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(addPanelJSON))
	cmd.SetArgs([]string{"obs", "dashboards", "add-panel", "ops", "--panel", "-", "--title", "Ops"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if d := readDashboard(t, dir, "ops"); d.Title != "Ops" || len(d.Panels) != 1 {
		t.Errorf("dashboard = %+v", d)
	}

	cmd = newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"obs", "dashboards", "add-panel", "ops"})
	if err := cmd.Execute(); err == nil {
		t.Error("--panel is required")
	}
}
