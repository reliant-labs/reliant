package statuspage

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"slices"
	"strings"
	"time"
)

// The files [Render] writes, by name.
const (
	FileIndex = "index.html"
	FileFeed  = "feed.atom"
	FileJSON  = "status.json"
)

// Output is a rendered page.
type Output struct {
	// Index is index.html.
	Index []byte
	// Feed is feed.atom.
	Feed []byte
	// JSON is status.json, the canonical document.
	JSON []byte
}

// Files is the output keyed by file name ([FileIndex], [FileFeed],
// [FileJSON]).
func (o Output) Files() map[string][]byte {
	return map[string][]byte{FileIndex: o.Index, FileFeed: o.Feed, FileJSON: o.JSON}
}

// Render validates d and renders its three files from its canonical form.
// The same document always gives the same bytes.
func Render(d Document) (Output, error) {
	if err := d.Validate(); err != nil {
		return Output{}, err
	}
	d = d.Canonical()
	js, err := marshal(d)
	if err != nil {
		return Output{}, err
	}
	html, err := renderHTML(d)
	if err != nil {
		return Output{}, err
	}
	feed, err := renderFeed(d)
	if err != nil {
		return Output{}, err
	}
	return Output{Index: html, Feed: feed, JSON: js}, nil
}

// Canonical is d in the one order a page is rendered and encoded in:
// times in UTC, incidents newest first, maintenance by start, updates
// oldest first, affected components in page order, and empty lists
// omitted. Unknown component references sort after known ones, by name.
// A copy: d is not modified.
func (d Document) Canonical() Document {
	out := d
	out.GeneratedAt = d.GeneratedAt.UTC()
	out.Page.AccentColor = strings.ToLower(d.Page.AccentColor)
	out.Page.Links = nilIfEmpty(slices.Clone(d.Page.Links))
	order := map[string]int{}
	out.Components = make([]Component, len(d.Components))
	for i, c := range d.Components {
		order[c.Name] = i
		c.Days = slices.Clone(c.Days)
		if c.Days == nil {
			c.Days = []Day{}
		}
		out.Components[i] = c
	}
	byPage := func(names []string) []string {
		names = nilIfEmpty(slices.Clone(names))
		slices.SortStableFunc(names, func(a, b string) int {
			ai, aKnown := order[a]
			bi, bKnown := order[b]
			switch {
			case aKnown && bKnown:
				return ai - bi
			case aKnown:
				return -1
			case bKnown:
				return 1
			default:
				return strings.Compare(a, b)
			}
		})
		return names
	}
	out.Incidents = nil
	for _, in := range d.Incidents {
		in.StartedAt = in.StartedAt.UTC()
		if in.ResolvedAt != nil {
			r := in.ResolvedAt.UTC()
			in.ResolvedAt = &r
		}
		in.Components = byPage(in.Components)
		in.Updates = slices.Clone(in.Updates)
		for i := range in.Updates {
			in.Updates[i].At = in.Updates[i].At.UTC()
		}
		slices.SortStableFunc(in.Updates, func(a, b IncidentUpdate) int { return a.At.Compare(b.At) })
		out.Incidents = append(out.Incidents, in)
	}
	slices.SortStableFunc(out.Incidents, func(a, b Incident) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	out.Maintenance = nil
	for _, m := range d.Maintenance {
		m.StartsAt, m.EndsAt = m.StartsAt.UTC(), m.EndsAt.UTC()
		m.Components = byPage(m.Components)
		m.Updates = slices.Clone(m.Updates)
		for i := range m.Updates {
			m.Updates[i].At = m.Updates[i].At.UTC()
		}
		slices.SortStableFunc(m.Updates, func(a, b MaintenanceUpdate) int { return a.At.Compare(b.At) })
		out.Maintenance = append(out.Maintenance, m)
	}
	slices.SortStableFunc(out.Maintenance, func(a, b Maintenance) int {
		if c := a.StartsAt.Compare(b.StartsAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}

// marshal is the canonical status.json encoding: two-space indent and a
// trailing newline.
func marshal(d Document) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Decode reads a status.json strictly: an unknown schema or an unknown
// field is refused, and the document must validate.
func Decode(data []byte) (Document, error) {
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if head.Schema != Schema {
		return Document{}, invalid("schema %q (this reader knows %q)", head.Schema, Schema)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d Document
	if err := dec.Decode(&d); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if dec.More() {
		return Document{}, invalid("trailing data after the document")
	}
	return d, d.Validate()
}

//go:embed index.html.tmpl
var indexTemplate string

var page = template.Must(template.New("index").Funcs(template.FuncMap{
	"icon":  icon,
	"label": label,
}).Parse(indexTemplate))

// icon is a state's glyph: each state has its own shape, so the state reads
// without colour.
func icon(s State) string {
	switch s {
	case StateOperational:
		return "✓"
	case StateDegraded:
		return "▲"
	case StateOutage:
		return "✕"
	case StateMaintenance:
		return "◆"
	default:
		return "○"
	}
}

// label is a state's word.
func label(s State) string {
	switch s {
	case StateOperational:
		return "Operational"
	case StateDegraded:
		return "Degraded"
	case StateOutage:
		return "Outage"
	case StateMaintenance:
		return "Maintenance"
	default:
		return "No data"
	}
}

// The page's colours. Every text colour meets WCAG AA (4.5:1) on the
// background it is drawn on; state colours are drawn as text on white and
// as white text on the banner.
const css = `:root{--accent:%s;--fg:#1f2328;--muted:#59636e;--line:#d1d9e0;--ok:#1a7f37;--warn:#9a6700;--bad:#cf222e;--maint:#0969da;--none:#59636e}
*{box-sizing:border-box}
body{margin:0;font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:var(--fg);background:#fff}
.wrap{max-width:52rem;margin:0 auto;padding:0 1rem}
.top{border-top:6px solid var(--accent);border-bottom:1px solid var(--line)}
.head{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:.5rem;padding-block:1rem}
h1{font-size:1.5rem;margin:0;display:flex;align-items:center;gap:.5rem}
.logo{height:2rem;width:auto}
.links{display:flex;flex-wrap:wrap;gap:1rem;list-style:none;margin:0;padding:0}
a{color:#0550ae}
h2{font-size:1.15rem;margin:2rem 0 .75rem}
h3{font-size:1rem;margin:0}
.banner{margin-top:1.5rem;border-radius:6px;color:#fff;font-weight:600;font-size:1.1rem}
.banner p{margin:0;padding:1rem 1.25rem}
.banner.s-operational{background:var(--ok)}.banner.s-degraded{background:var(--warn)}.banner.s-outage{background:var(--bad)}.banner.s-maintenance{background:var(--maint)}.banner.s-no_data{background:var(--none)}
.components{list-style:none;margin:0;padding:0;border:1px solid var(--line);border-radius:6px}
.component{padding:1rem 1.25rem}
.component+.component{border-top:1px solid var(--line)}
.row{display:flex;justify-content:space-between;align-items:flex-start;gap:1rem}
.desc{margin:.1rem 0 0;color:var(--muted);font-size:.9rem}
.state{margin:0;font-weight:600;white-space:nowrap}
.s-operational{color:var(--ok)}.s-degraded{color:var(--warn)}.s-outage{color:var(--bad)}.s-maintenance{color:var(--maint)}.s-no_data{color:var(--none)}
.note{font-weight:400;color:var(--muted)}
.bars{display:flex;gap:2px;list-style:none;margin:.75rem 0 0;padding:0;height:2rem}
.bar{flex:1;border-radius:2px;min-width:2px}
.b-ok{background:var(--ok)}.b-warn{background:#bf8700}.b-bad{background:var(--bad)}.b-none{background:#d1d9e0}
.b-maint{box-shadow:inset 0 -6px 0 var(--maint)}
.summary{display:flex;justify-content:space-between;margin:.25rem 0 0;color:var(--muted);font-size:.8rem}
.notice{border:1px solid var(--line);border-radius:6px;padding:1rem 1.25rem;margin-bottom:.75rem}
.meta{margin:.25rem 0 0;color:var(--muted);font-size:.9rem}
.impact{font-weight:600}.i-minor{color:var(--warn)}.i-major,.i-critical{color:var(--bad)}.i-maint{color:var(--maint)}
.updates{list-style:none;margin:.75rem 0 0;padding:0}
.updates li+li{margin-top:.75rem}
.updates p{margin:.25rem 0 0}
.ustatus{text-transform:capitalize}
.foot{color:var(--muted);font-size:.85rem;padding-block:2rem}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
`

// defaultAccent decorates a page that declares no accent colour.
const defaultAccent = "#59636e"

type view struct {
	Doc          Document
	CSS          template.CSS
	CSP          string
	Overall      Overall
	Components   []componentView
	Active       []incidentView
	History      []incidentView
	Upcoming     []maintenanceView
	Completed    []maintenanceView
	Generated    string
	GeneratedISO string
}

type componentView struct {
	Component
	Bars    []barView
	Span    string
	Summary string
}

type barView struct {
	Class       string
	Title       string
	Maintenance bool
}

type updateView struct {
	Status     string
	At         string
	ISO        string
	Paragraphs [][]string
}

type incidentView struct {
	ID          string
	Title       string
	Impact      Impact
	ImpactLabel string
	Components  string
	When        string
	Updates     []updateView
}

type maintenanceView struct {
	ID          string
	Title       string
	StatusLabel string
	Components  string
	When        string
	Updates     []updateView
}

const (
	clock   = "2006-01-02 15:04 UTC"
	isoTime = time.RFC3339
)

func renderHTML(d Document) ([]byte, error) {
	accent := d.Page.AccentColor
	if accent == "" {
		accent = defaultAccent
	}
	style := fmt.Sprintf(css, accent)
	sum := sha256.Sum256([]byte(style))
	v := view{
		Doc: d,
		// The style is this package's constant plus a validated #rrggbb.
		CSS: template.CSS(style), //nolint:gosec // G203: no input reaches it but the regexp-checked accent colour
		CSP: "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
			"'; img-src https:; base-uri 'none'; form-action 'none'",
		Overall:      OverallOf(d.Components),
		Generated:    d.GeneratedAt.Format(clock),
		GeneratedISO: d.GeneratedAt.Format(isoTime),
	}
	for _, c := range d.Components {
		v.Components = append(v.Components, componentOf(c))
	}
	for _, in := range d.Incidents {
		iv := incidentOf(in)
		if in.Status == IncidentResolved {
			v.History = append(v.History, iv)
		} else {
			v.Active = append(v.Active, iv)
		}
	}
	for _, m := range d.Maintenance {
		mv := maintenanceOf(m)
		if m.Status == MaintenanceCompleted {
			v.Completed = append([]maintenanceView{mv}, v.Completed...) // newest first
		} else {
			v.Upcoming = append(v.Upcoming, mv)
		}
	}
	var buf bytes.Buffer
	if err := page.Execute(&buf, v); err != nil {
		return nil, fmt.Errorf("render index.html: %w", err)
	}
	return buf.Bytes(), nil
}

// percent is an availability as a percentage, rounded DOWN to two places:
// a day with one failed probe in ten thousand reads 99.99%, never 100%.
func percent(a float64) string {
	if a >= 1 {
		return "100%"
	}
	return fmt.Sprintf("%.2f%%", math.Floor(a*10000)/100)
}

func componentOf(c Component) componentView {
	cv := componentView{Component: c}
	var total float64
	var measured int
	for _, day := range c.Days {
		b := barView{Maintenance: day.Maintenance}
		switch a := day.Availability; {
		case a == nil:
			b.Class, b.Title = "none", day.Date+": no data"
		default:
			total += *a
			measured++
			b.Title = day.Date + ": " + percent(*a) + " available"
			switch {
			case *a >= 0.999:
				b.Class = "ok"
			case *a >= 0.99:
				b.Class = "warn"
			default:
				b.Class = "bad"
			}
		}
		if day.Maintenance {
			b.Title += ", maintenance"
		}
		cv.Bars = append(cv.Bars, b)
	}
	if n := len(c.Days); n > 0 {
		cv.Span = fmt.Sprintf("%d days ago", n-1)
		if n == 1 {
			cv.Span = "Today"
		}
	}
	if measured > 0 {
		cv.Summary = percent(total/float64(measured)) + " average daily availability"
	} else {
		cv.Summary = "No data"
	}
	return cv
}

// paragraphs splits a plain-text body into paragraphs (at blank lines) of
// lines. The template escapes every line; nothing in a body is markup.
func paragraphs(body string) [][]string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out [][]string
	for _, p := range strings.Split(body, "\n\n") {
		p = strings.Trim(p, "\n")
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, strings.Split(p, "\n"))
	}
	return out
}

func words(s string) string { return strings.ReplaceAll(s, "_", " ") }

func capitalized(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func incidentOf(in Incident) incidentView {
	iv := incidentView{
		ID: in.ID, Title: in.Title, Impact: in.Impact, ImpactLabel: capitalized(string(in.Impact)),
		Components: strings.Join(in.Components, ", "),
		When:       "Started " + in.StartedAt.Format(clock),
	}
	if in.ResolvedAt != nil {
		iv.When += ", resolved " + in.ResolvedAt.Format(clock)
	}
	for i := len(in.Updates) - 1; i >= 0; i-- { // newest first
		u := in.Updates[i]
		iv.Updates = append(iv.Updates, updateView{Status: string(u.Status), At: u.At.Format(clock), ISO: u.At.Format(isoTime), Paragraphs: paragraphs(u.Body)})
	}
	return iv
}

func maintenanceOf(m Maintenance) maintenanceView {
	mv := maintenanceView{
		ID: m.ID, Title: m.Title, StatusLabel: capitalized(words(string(m.Status))),
		Components: strings.Join(m.Components, ", "),
		When:       m.StartsAt.Format(clock) + " to " + m.EndsAt.Format(clock),
	}
	for i := len(m.Updates) - 1; i >= 0; i-- {
		u := m.Updates[i]
		mv.Updates = append(mv.Updates, updateView{Status: words(string(u.Status)), At: u.At.Format(clock), ISO: u.At.Format(isoTime), Paragraphs: paragraphs(u.Body)})
	}
	return mv
}
