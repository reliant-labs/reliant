package cli

// `forge obs dashboards add-panel` — the dashboards-as-code half of "save this
// query to a dashboard": append (or replace) one panel in
// deploy/observability/dashboards/<slug>.json, creating the dashboard when it
// does not exist yet. The panel is validated by dashspec, the same validator
// `forge obs dashboards check` and a release cut run, and the file is written
// in the one canonical form, so a second identical run changes nothing.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsdash"
)

// dashboardSlugPattern is the slug rule of a control-plane workspace dashboard
// (ck_cp_ws_dashboards_slug), so a slug means the same thing in both homes.
var dashboardSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type obsDashboardsAddOpts struct {
	slug    string
	panel   string
	title   string
	id      string
	replace bool
}

func newObsDashboardsAddPanelCmd() *cobra.Command {
	var o obsDashboardsAddOpts
	cmd := &cobra.Command{
		Use:   "add-panel <slug>",
		Short: "Append a panel to a dashboard you own, creating it when missing",
		Long: `Add one panel to deploy/observability/dashboards/<slug>.json, the dashboard a
release ships for the Reliant console. The file is created when it does not
exist (id is the slug; title is --title, or the slug).

--panel is a file, or "-" for stdin, holding ONE panel object in the dashboard
vocabulary (for example the document 'forge obs metrics query --panel-out'
writes). It is validated exactly as 'forge obs dashboards check' validates it,
and an error carries the panel file's own line and column.

The panel's id comes from --id, else the panel's own "id", else is made from its
title and made unique within the dashboard. An id the dashboard already holds is
an error unless --replace, which swaps that panel in place. Nothing is written
when anything fails, and a repeated run with the same inputs leaves the file
byte-identical.

A slug equal to a generated dashboard's name (shop-overview for
shop-overview_gen.json) replaces that dashboard in releases; a warning says so.`,
		Example: `  forge obs dashboards add-panel checkout --panel p95.json --title "Checkout"
  forge obs metrics query prod orders.created --panel-out - | forge obs dashboards add-panel checkout --panel -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.slug = args[0]
			return runObsDashboardsAddPanel(projectDirForKCL(), o, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.panel, "panel", "", `file holding one panel object, or "-" for stdin (required)`)
	f.StringVar(&o.title, "title", "", "title of a dashboard being created; default: the slug")
	f.StringVar(&o.id, "id", "", "id for the panel, overriding the panel's own")
	f.BoolVar(&o.replace, "replace", false, "replace the panel that has the same id instead of failing")
	_ = cmd.MarkFlagRequired("panel")
	return cmd
}

func runObsDashboardsAddPanel(projectDir string, o obsDashboardsAddOpts, stdin io.Reader, out, errOut io.Writer) error {
	if !dashboardSlugPattern.MatchString(o.slug) {
		return fmt.Errorf("%q is not a valid dashboard slug: lowercase letters, digits and '-', starting with a letter or digit, at most 63 characters", o.slug)
	}
	if o.id != "" && !dashboardPanelIDPattern.MatchString(o.id) {
		return fmt.Errorf("--id %q is not a valid panel id: letters, digits, '-', '_' and '.', starting with a letter or digit", o.id)
	}

	name, raw, err := readPanelInput(o.panel, stdin)
	if err != nil {
		return err
	}
	panel, err := validatePanel(name, raw, o.id != "")
	if err != nil {
		return err
	}
	if o.id != "" {
		panel.ID = o.id
	}

	rel := obsdash.DashboardsDir + "/" + o.slug + ".json"
	path := filepath.Join(projectDir, filepath.FromSlash(rel))
	dash, existed, err := loadOwnedDashboard(rel, path)
	if err != nil {
		return err
	}
	if !existed {
		dash = obsdash.Dashboard{ID: o.slug, Title: o.slug, Panels: []obsdash.Panel{}}
		if o.title != "" {
			dash.Title = o.title
		}
	}

	if panel.ID == "" {
		if o.replace {
			return fmt.Errorf("--replace needs the id of the panel to replace: pass --id, or give the panel an \"id\"")
		}
		panel.ID = uniquePanelID(dash.Panels, panel.Title)
	}
	at := -1
	for i, p := range dash.Panels {
		if p.ID == panel.ID {
			at = i
		}
	}
	switch {
	case at >= 0 && !o.replace:
		return fmt.Errorf("%s already has a panel with id %q; pass --replace to replace it, or --id to add this one under another id", rel, panel.ID)
	case at >= 0:
		dash.Panels[at] = panel
	default:
		dash.Panels = append(dash.Panels, panel)
	}

	if err := checkAgainstGenerated(projectDir, o.slug, dash.ID, errOut); err != nil {
		return err
	}

	data, err := marshalOwnedDashboard(dash)
	if err != nil {
		return err
	}
	if _, err := obsdash.ParseDashboard(rel, data); err != nil {
		return err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}

	verb := "added"
	if at >= 0 {
		verb = "replaced"
	}
	fmt.Fprintf(out, "%s panel %q (%d panel(s))\n%s\n", verb, panel.ID, len(dash.Panels), rel)
	fmt.Fprintln(out, "suggested: forge obs dashboards check")
	return nil
}

var dashboardPanelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func readPanelInput(panel string, stdin io.Reader) (name string, raw []byte, err error) {
	if panel == "-" {
		raw, err = io.ReadAll(stdin)
		return "<stdin>", raw, err
	}
	raw, err = os.ReadFile(panel)
	return panel, raw, err
}

// panelWrapperPrefix wraps a panel as the one panel of a dashboard WITHOUT a
// newline, so line N of the panel is line N of the wrapper and only line 1's
// columns shift, by len(panelWrapperPrefix).
const panelWrapperPrefix = `{"id":"p","title":"p","panels":[`

// validatePanel checks one panel with dashspec's own validator and reports each
// problem at the panel file's position. A panel with no id is valid here (the
// command gives it one), and so is any id problem when --id overrides it.
func validatePanel(name string, raw []byte, idOverridden bool) (obsdash.Panel, error) {
	wrapped := append(append([]byte(panelWrapperPrefix), raw...), "\n]}"...)
	_, err := obsdash.ParseDashboard(name, wrapped)
	if err != nil {
		var lines []string
		for _, p := range obsdash.ProblemsOf(err) {
			if p.Path == "panels[0]" && strings.Contains(p.Message, `"id"`) && strings.HasPrefix(p.Message, "missing required field") {
				continue
			}
			if idOverridden && p.Path == "panels[0].id" {
				continue
			}
			lines = append(lines, formatPanelProblem(name, p))
		}
		if len(lines) > 0 {
			return obsdash.Panel{}, errors.New(strings.Join(lines, "\n"))
		}
		if len(obsdash.ProblemsOf(err)) == 0 {
			return obsdash.Panel{}, err
		}
	}
	var panel obsdash.Panel
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&panel); err != nil {
		return obsdash.Panel{}, fmt.Errorf("%s: the panel must be one JSON object: %w", name, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return obsdash.Panel{}, fmt.Errorf("%s: the panel must be exactly one JSON object, with nothing after it", name)
	}
	return panel, nil
}

func formatPanelProblem(name string, p obsdash.Problem) string {
	loc := name
	if p.Line > 0 {
		col := p.Col
		if p.Line == 1 {
			col -= len(panelWrapperPrefix)
		}
		if col < 1 {
			col = 1
		}
		loc = name + ":" + strconv.Itoa(p.Line) + ":" + strconv.Itoa(col)
	}
	path := strings.TrimPrefix(strings.TrimPrefix(p.Path, "panels[0]"), ".")
	if path != "" {
		return fmt.Sprintf("%s: %s: %s", loc, path, p.Message)
	}
	return fmt.Sprintf("%s: %s", loc, p.Message)
}

// loadOwnedDashboard reads the dashboard file when it exists. A HyperDX file is
// refused rather than overwritten: it is the local stack's, in another shape.
func loadOwnedDashboard(rel, path string) (obsdash.Dashboard, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return obsdash.Dashboard{}, false, nil
	}
	if err != nil {
		return obsdash.Dashboard{}, false, err
	}
	if obsdash.LooksLikeHyperDXDashboard(raw) {
		return obsdash.Dashboard{}, false, fmt.Errorf("%s is a HyperDX dashboard (it has \"tiles\"), not a console dashboard; add-panel will not rewrite it. Pick another slug", rel)
	}
	d, err := obsdash.ParseDashboard(rel, raw)
	if err != nil {
		return obsdash.Dashboard{}, false, err
	}
	return d, true, nil
}

// checkAgainstGenerated warns when the file would replace a generated
// dashboard (same name), and refuses when its id collides with a generated
// dashboard it does not replace: the console keys a dashboard on its id, so the
// release cut would fail on it.
func checkAgainstGenerated(projectDir, slug, id string, errOut io.Writer) error {
	sp, ok, err := obsdash.ReadSpec(projectDir)
	if err != nil || !ok {
		return nil
	}
	for _, g := range sp.Dashboards {
		name := strings.TrimSuffix(obsdash.DashboardFile(sp.Project, g), "_gen.json")
		switch {
		case name == slug:
			fmt.Fprintf(errOut, "warning: %s.json replaces the generated dashboard %q that your releases ship (a project file named like a generated one overrides it)\n", slug, g.Title)
		case g.ID == id:
			return fmt.Errorf("dashboard id %q is already used by the generated dashboard %q (%s); the console keys a dashboard on its id, so pick another slug", id, g.Title, name)
		}
	}
	return nil
}

// ownedDashboardFile is the canonical on-disk form: the spec version, then the
// dashboard, the shape every generated dashboard file is written in.
type ownedDashboardFile struct {
	Version string `json:"version"`
	obsdash.Dashboard
}

func marshalOwnedDashboard(d obsdash.Dashboard) ([]byte, error) {
	b, err := json.MarshalIndent(ownedDashboardFile{Version: obsdash.SpecVersion, Dashboard: d}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(bytes.TrimRight(b, "\n"), '\n'), nil
}

// uniquePanelID makes an id from title ("Order rate" is "order-rate") that no
// panel of the dashboard holds, suffixing -2, -3, ... on a clash.
func uniquePanelID(panels []obsdash.Panel, title string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "panel"
	}
	taken := map[string]bool{}
	for _, p := range panels {
		taken[p.ID] = true
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	return id
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".add-panel-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
