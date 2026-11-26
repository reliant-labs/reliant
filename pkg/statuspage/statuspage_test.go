package statuspage

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"flag"
	stdhtml "html"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func avail(a float64) *float64 { return &a }

// days is n daily bars ending at end, every one at a, except the ones
// overridden by name.
func days(end string, n int, a float64, override map[string]*float64) []Day {
	last, err := time.Parse(time.DateOnly, end)
	if err != nil {
		panic(err)
	}
	out := make([]Day, n)
	for i := range out {
		date := last.AddDate(0, 0, i-n+1).Format(time.DateOnly)
		out[i] = Day{Date: date, Availability: avail(a)}
		if v, ok := override[date]; ok {
			out[i].Availability = v
		}
	}
	return out
}

// fullDoc uses every field of the schema.
func fullDoc() Document {
	resolved := at("2026-11-20T15:10:00Z")
	web := days("2026-11-25", 90, 1, map[string]*float64{"2026-11-20": avail(0.9583), "2026-11-01": nil})
	web[80].Maintenance = true
	return Document{
		Schema:      Schema,
		GeneratedAt: at("2026-11-25T14:05:00+01:00"),
		Page: Page{
			Title:       "Acme status",
			URL:         "https://status.acme.test/",
			LogoURL:     "https://acme.test/logo.svg",
			AccentColor: "#5B21B6",
			Links:       []Link{{Label: "Support", URL: "https://acme.test/help"}, {Label: "Acme", URL: "https://acme.test"}},
		},
		Components: []Component{
			{Name: "Website", State: StateOperational, Days: web},
			{Name: "API", Description: "REST and webhooks", State: StateOutage, Days: days("2026-11-25", 90, 0.9995, map[string]*float64{"2026-11-25": avail(0.42)})},
			{Name: "Checkout", State: StateDegraded, Overridden: true, Days: days("2026-11-25", 3, 0.995, nil)},
			{Name: "Search", State: StateNoData},
		},
		Incidents: []Incident{
			{
				ID: "inc-2", Title: "API is unavailable", Impact: ImpactMajor, Status: IncidentIdentified,
				Components: []string{"Checkout", "API"}, StartedAt: at("2026-11-25T13:02:00Z"),
				Updates: []IncidentUpdate{
					{Status: IncidentIdentified, Body: "A database migration locked the orders table.\n\nWe are rolling it back.", At: at("2026-11-25T13:30:00Z")},
					{Status: IncidentInvestigating, Body: "API requests are failing.\nWe are looking into it.", At: at("2026-11-25T13:05:00Z")},
				},
			},
			{
				ID: "inc-1", Title: "Slow page loads", Impact: ImpactMinor, Status: IncidentResolved,
				Components: []string{"Website"}, StartedAt: at("2026-11-20T14:00:00Z"), ResolvedAt: &resolved,
				Updates: []IncidentUpdate{
					{Status: IncidentInvestigating, Body: "Pages load slowly <for some> users & we know.", At: at("2026-11-20T14:05:00Z")},
					{Status: IncidentResolved, Body: "Fixed.", At: resolved},
				},
			},
		},
		Maintenance: []Maintenance{
			{
				ID: "m-2", Title: "Database upgrade", Status: MaintenanceScheduled, Components: []string{"API"},
				StartsAt: at("2026-11-28T02:00:00Z"), EndsAt: at("2026-11-28T03:00:00Z"),
				Updates: []MaintenanceUpdate{{Status: MaintenanceScheduled, Body: "The API may be read-only for up to an hour.", At: at("2026-11-24T09:00:00Z")}},
			},
			{
				ID: "m-1", Title: "Network work", Status: MaintenanceCompleted, Components: []string{"Website"},
				StartsAt: at("2026-11-15T02:00:00Z"), EndsAt: at("2026-11-15T02:30:00Z"),
				Updates: []MaintenanceUpdate{
					{Status: MaintenanceScheduled, Body: "Brief interruptions.", At: at("2026-11-10T09:00:00Z")},
					{Status: MaintenanceCompleted, Body: "Done.", At: at("2026-11-15T02:25:00Z")},
				},
			},
		},
	}
}

// minimalDoc is a page with nothing but components: no URL, no incidents.
func minimalDoc() Document {
	return Document{
		Schema:      Schema,
		GeneratedAt: at("2026-11-25T14:00:00Z"),
		Page:        Page{Title: "Status"},
		Components: []Component{
			{Name: "Website", State: StateOperational, Days: days("2026-11-25", 7, 1, nil)},
		},
	}
}

var update = flag.Bool("update", false, "rewrite testdata/ from the current render")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with go test ./pkg/statuspage -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (regenerate with go test ./pkg/statuspage -update after checking the change):\n%s", name, got)
	}
}

// TestRenderGolden pins the bytes of all three files for a full and a
// minimal page.
func TestRenderGolden(t *testing.T) {
	for name, d := range map[string]Document{"full": fullDoc(), "minimal": minimalDoc()} {
		out, err := Render(d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		golden(t, name+".index.html", out.Index)
		golden(t, name+".feed.atom", out.Feed)
		golden(t, name+".status.json", out.JSON)
	}
}

// TestRenderIsDeterministic: input order does not change the bytes, and
// rendering twice gives the same bytes.
func TestRenderIsDeterministic(t *testing.T) {
	a, err := Render(fullDoc())
	if err != nil {
		t.Fatal(err)
	}
	shuffled := fullDoc()
	shuffled.Incidents[0], shuffled.Incidents[1] = shuffled.Incidents[1], shuffled.Incidents[0]
	shuffled.Maintenance[0], shuffled.Maintenance[1] = shuffled.Maintenance[1], shuffled.Maintenance[0]
	shuffled.Incidents[1].Components = []string{"API", "Checkout"}
	b, err := Render(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("the same document in another order rendered different bytes")
	}
}

// TestStatusJSONRoundTrips: status.json decodes to the canonical document,
// and re-rendering it gives the same files.
func TestStatusJSONRoundTrips(t *testing.T) {
	out, err := Render(fullDoc())
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(out.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, fullDoc().Canonical()) {
		t.Errorf("status.json decodes to\n%+v\nwant\n%+v", back, fullDoc().Canonical())
	}
	again, err := Render(back)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, out) {
		t.Error("rendering the decoded status.json changed the files")
	}
}

func TestDecodeRefuses(t *testing.T) {
	out, err := Render(minimalDoc())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"unknown schema": strings.Replace(string(out.JSON), Schema, "forge.status/v2", 1),
		"unknown field":  strings.Replace(string(out.JSON), `"schema"`, `"check_url": "https://x.test", "schema"`, 1),
		"not json":       "<html>",
		"trailing data":  string(out.JSON) + "{}",
		"invalid":        strings.Replace(string(out.JSON), `"operational"`, `"up"`, 1),
	} {
		if _, err := Decode([]byte(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Decode = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := Decode([]byte(strings.Replace(string(out.JSON), Schema, "forge.status/v2", 1))); err == nil || !strings.Contains(err.Error(), "forge.status/v2") {
		t.Errorf("an unknown schema is refused by name, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	if err := fullDoc().Validate(); err != nil {
		t.Fatalf("the full document: %v", err)
	}
	for name, tc := range map[string]struct {
		edit func(*Document)
		want string
	}{
		"schema":                {func(d *Document) { d.Schema = "forge.status/v0" }, "schema"},
		"no time":               {func(d *Document) { d.GeneratedAt = time.Time{} }, "generated_at"},
		"no title":              {func(d *Document) { d.Page.Title = "" }, "title is empty"},
		"long title":            {func(d *Document) { d.Page.Title = strings.Repeat("a", MaxTitle+1) }, "longer than"},
		"title control":         {func(d *Document) { d.Page.Title = "a\nb" }, "control character"},
		"http logo":             {func(d *Document) { d.Page.LogoURL = "http://acme.test/logo.png" }, "must be https"},
		"javascript link":       {func(d *Document) { d.Page.Links[0].URL = "javascript:alert(1)" }, "http or https"},
		"credential link":       {func(d *Document) { d.Page.Links[0].URL = "https://u:p@acme.test" }, "credential"},
		"relative url":          {func(d *Document) { d.Page.URL = "/status" }, "http or https"},
		"accent":                {func(d *Document) { d.Page.AccentColor = "red" }, "#rrggbb"},
		"accent injection":      {func(d *Document) { d.Page.AccentColor = "#fff;}body{" }, "#rrggbb"},
		"too many links":        {func(d *Document) { d.Page.Links = make([]Link, MaxLinks+1) }, "links"},
		"no components":         {func(d *Document) { d.Components = nil }, "no components"},
		"duplicate component":   {func(d *Document) { d.Components[1].Name = "Website" }, "named twice"},
		"state":                 {func(d *Document) { d.Components[0].State = "up" }, "state"},
		"long description":      {func(d *Document) { d.Components[0].Description = strings.Repeat("a", MaxDescription+1) }, "longer than"},
		"too many days":         {func(d *Document) { d.Components[0].Days = days("2026-11-25", MaxDays+1, 1, nil) }, "days"},
		"bad date":              {func(d *Document) { d.Components[0].Days[0].Date = "2026-02-30" }, "YYYY-MM-DD"},
		"days out of order":     {func(d *Document) { d.Components[0].Days[1].Date = d.Components[0].Days[0].Date }, "oldest first"},
		"availability":          {func(d *Document) { d.Components[0].Days[0].Availability = avail(1.5) }, "outside 0..1"},
		"incident id":           {func(d *Document) { d.Incidents[0].ID = "a b" }, "incident id"},
		"duplicate incident":    {func(d *Document) { d.Incidents[1].ID = d.Incidents[0].ID }, "used twice"},
		"impact":                {func(d *Document) { d.Incidents[0].Impact = "huge" }, "impact"},
		"incident status":       {func(d *Document) { d.Incidents[0].Status = "fixed" }, "status"},
		"unknown component":     {func(d *Document) { d.Incidents[0].Components = []string{"Billing"} }, `component "Billing"`},
		"resolved without time": {func(d *Document) { d.Incidents[1].ResolvedAt = nil }, "no resolved_at"},
		"open with time":        {func(d *Document) { d.Incidents[0].ResolvedAt = &d.GeneratedAt }, "has resolved_at"},
		"no update":             {func(d *Document) { d.Incidents[0].Updates = nil }, "no published update"},
		"empty body":            {func(d *Document) { d.Incidents[0].Updates[0].Body = " \n" }, "body is empty"},
		"long body":             {func(d *Document) { d.Incidents[0].Updates[0].Body = strings.Repeat("a", MaxUpdateBody+1) }, "longer than"},
		"maintenance order":     {func(d *Document) { d.Maintenance[0].EndsAt = d.Maintenance[0].StartsAt }, "ends before"},
		"maintenance status":    {func(d *Document) { d.Maintenance[0].Status = "done" }, "status"},
		"maintenance component": {func(d *Document) { d.Maintenance[0].Components = []string{"Billing"} }, `component "Billing"`},
		"maintenance no update": {func(d *Document) { d.Maintenance[0].Updates = nil }, "no published update"},
	} {
		d := fullDoc()
		tc.edit(&d)
		err := d.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate = %v, want ErrInvalid naming %q", name, err, tc.want)
		}
		if _, rerr := Render(d); rerr == nil {
			t.Errorf("%s: Render accepted an invalid document", name)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{
		"https://acme.test",
		"https://acme.test/",
		"http://acme.test:8080/status",
		"https://status.acme.test:443/a/b?x=1&y=two#frag",
		"https://acme.test.",
		"https://a-b.c-d.test/path",
		"http://192.0.2.10/",
		"http://192.0.2.10:65535/",
		"https://[2001:db8::1]/",
		"https://[2001:db8::1]:8443/x?y=z",
		"https://acme.test/search?q=a%20b",
	} {
		if err := ValidateURL(raw, false); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
	for raw, want := range map[string]string{
		"https://acme.test:99999/":                      "port",
		"https://acme.test:65536/":                      "port",
		"https://acme.test:0/":                          "port",
		"https://acme.test:abc/":                        "acme.test:abc",
		"https://acme.test:/":                           "empty port",
		"https://acme.test:-1/":                         "acme.test:-1",
		"https://acme.test:080/":                        "port",
		"https://acme_test/":                            "host",
		"https://-acme.test/":                           "host",
		"https://acme-.test/":                           "host",
		"https://acme..test/":                           "host",
		"https://a%2Fb.test/":                           "invalid URL escape",
		"https://999.1.1.1/":                            "host",
		"https://[192.0.2.10]/":                         "invalid IP-literal",
		"https://[2001:db8::zz]/":                       "2001:db8::zz",
		"https://[2001:db8::1/":                         "2001:db8::1",
		"https://:443/":                                 "no host",
		"https:///path":                                 "no host",
		"https:acme.test":                               "absolute",
		" https://acme.test":                            "whitespace",
		"https://acme.test ":                            "whitespace",
		"https://acme .test/":                           "whitespace",
		"https://acme.test/a b":                         "whitespace",
		"https://acme.test/\u00a0":                      "whitespace",
		"https://acme.test/\t":                          "whitespace",
		"https://acme.test/\n":                          "whitespace",
		"https://acme.test/\x00":                        "control",
		"https://acme.test/\x7f":                        "control",
		"https://acme.test/\u0085":                      "control",
		"https://acme.test/\xff":                        "UTF-8",
		"https://u@acme.test/":                          "credential",
		"ftp://acme.test/":                              "http or https",
		"//acme.test/":                                  "http or https",
		"https://" + strings.Repeat("a", 64) + ".test/": "host",
	} {
		if err := ValidateURL(raw, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateURL(%q) = %v, want an error naming %q", raw, err, want)
		}
	}
	if err := ValidateURL("http://acme.test/logo.png", true); err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Errorf("an http logo is refused when https is required, got %v", err)
	}
}

// TestHTMLIsSelfContained: no script, nothing loaded from elsewhere but the
// declared logo, and the CSP's style hash is the style the page carries.
func TestHTMLIsSelfContained(t *testing.T) {
	out, err := Render(fullDoc())
	if err != nil {
		t.Fatal(err)
	}
	html := string(out.Index)
	if strings.Contains(strings.ToLower(html), "<script") {
		t.Error("index.html carries a script")
	}
	for _, m := range regexp.MustCompile(`src="([^"]*)"`).FindAllStringSubmatch(html, -1) {
		if m[1] != "https://acme.test/logo.svg" {
			t.Errorf("index.html loads %q", m[1])
		}
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(html)
	csp := regexp.MustCompile(`style-src 'sha256-([^']+)'`).FindStringSubmatch(stdhtml.UnescapeString(html))
	if style == nil || csp == nil {
		t.Fatal("index.html has no style or no CSP style hash")
	}
	sum := sha256.Sum256([]byte(style[1]))
	if got := base64.StdEncoding.EncodeToString(sum[:]); got != csp[1] {
		t.Errorf("CSP style hash %s, the style hashes to %s: a browser would refuse the page's own CSS", csp[1], got)
	}
	if !strings.Contains(style[1], "--accent:#5b21b6") {
		t.Error("the accent colour does not reach the page")
	}
}

// TestBodiesAreEscaped: a body is text in every file, never markup.
func TestBodiesAreEscaped(t *testing.T) {
	d := minimalDoc()
	d.Incidents = []Incident{{
		ID: "x", Title: `<img src=x onerror=alert(1)>`, Impact: ImpactMinor, Status: IncidentInvestigating,
		StartedAt: at("2026-11-25T13:00:00Z"),
		Updates:   []IncidentUpdate{{Status: IncidentInvestigating, Body: "<script>alert(1)</script>\n\n**bold** & [link](javascript:x)", At: at("2026-11-25T13:01:00Z")}},
	}}
	out, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out.Index)
	for _, bad := range []string{"<script>", "<img src=x", "<strong>bold", `href="javascript`} {
		if strings.Contains(html, bad) {
			t.Errorf("index.html contains %q unescaped", bad)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(html, "**bold** &amp; [link](javascript:x)") {
		t.Errorf("index.html does not show the body as text:\n%s", html)
	}
	var feed struct {
		Entries []struct {
			Title   string `xml:"title"`
			Content struct {
				Type string `xml:"type,attr"`
				Body string `xml:",chardata"`
			} `xml:"content"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(out.Feed, &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Entries) != 1 || feed.Entries[0].Content.Type != "text" || !strings.Contains(feed.Entries[0].Content.Body, "<script>alert(1)</script>") {
		t.Errorf("the feed does not carry the body as plain text: %+v", feed.Entries)
	}
	if strings.Contains(string(out.Feed), "<script>") {
		t.Error("feed.atom carries the body unescaped")
	}
}

// TestEveryStateHasAnIconAndAWord: a state never reads by colour alone.
func TestEveryStateHasAnIconAndAWord(t *testing.T) {
	icons, labels := map[string]State{}, map[string]State{}
	for _, s := range States {
		if prev, ok := icons[icon(s)]; ok {
			t.Errorf("%s and %s share the icon %q", s, prev, icon(s))
		}
		if prev, ok := labels[label(s)]; ok {
			t.Errorf("%s and %s share the word %q", s, prev, label(s))
		}
		icons[icon(s)], labels[label(s)] = s, s
	}
	out, err := Render(fullDoc())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fullDoc().Components {
		if !strings.Contains(string(out.Index), icon(c.State)+`</span> `+label(c.State)) {
			t.Errorf("component %s is not drawn with its icon and word", c.Name)
		}
	}
}

func TestPercentRoundsDown(t *testing.T) {
	for a, want := range map[float64]string{1: "100%", 0.99999: "99.99%", 0.5: "50.00%", 0: "0.00%", 0.95834: "95.83%"} {
		if got := percent(a); got != want {
			t.Errorf("percent(%v) = %s, want %s", a, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	for name, tc := range map[string]struct {
		measured    []State
		maintenance bool
		override    State
		want        State
		overridden  bool
	}{
		"all up":                {[]State{StateOperational, StateOperational}, false, "", StateOperational, false},
		"worst wins":            {[]State{StateOperational, StateDegraded, StateOutage}, false, "", StateOutage, false},
		"silence is not health": {[]State{StateOperational, StateNoData}, false, "", StateNoData, false},
		"degraded over silence": {[]State{StateNoData, StateDegraded}, false, "", StateDegraded, false},
		"no checks":             {nil, false, "", StateNoData, false},
		"unknown is no data":    {[]State{StateOperational, "weird"}, false, "", StateNoData, false},
		"maintenance wins":      {[]State{StateOutage}, true, "", StateMaintenance, false},
		"override wins":         {[]State{StateOutage}, true, StateOperational, StateOperational, true},
	} {
		got, over := Resolve(tc.measured, tc.maintenance, tc.override)
		if got != tc.want || over != tc.overridden {
			t.Errorf("%s: Resolve = %s, %v; want %s, %v", name, got, over, tc.want, tc.overridden)
		}
	}
}

func TestOverallOf(t *testing.T) {
	c := func(states ...State) []Component {
		var out []Component
		for _, s := range states {
			out = append(out, Component{State: s})
		}
		return out
	}
	for want, comps := range map[string][]Component{
		"All systems operational":          c(StateOperational, StateOperational),
		"Partial outage":                   c(StateOperational, StateOutage, StateDegraded),
		"Major outage":                     c(StateOutage, StateOutage),
		"Degraded performance":             c(StateDegraded, StateMaintenance),
		"Maintenance in progress":          c(StateOperational, StateMaintenance),
		"No data yet":                      c(StateNoData),
		"Some systems have no recent data": c(StateOperational, StateNoData),
	} {
		if got := OverallOf(comps).Message; got != want {
			t.Errorf("OverallOf(%v) = %q, want %q", comps, got, want)
		}
	}
}
