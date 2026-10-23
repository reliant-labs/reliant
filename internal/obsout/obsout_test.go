package obsout

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

var update = flag.Bool("update", false, "rewrite the golden files")

type coded struct {
	code int
	msg  string
}

func (c coded) Error() string { return c.msg }
func (c coded) ExitCode() int { return c.code }

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
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

type entry struct {
	Service string            `json:"service"`
	Body    string            `json:"body"`
	Attrs   map[string]string `json:"attributes,omitempty"`
}

type doc struct {
	Head
	Scope   Scope   `json:"scope"`
	Entries []entry `json:"entries"`
	Page
	Suggested []string `json:"suggested,omitempty"`
}

var docSchema = Schema{
	Name:   "forge.obs.test/v1",
	Fields: []string{"scope", "scope.env", "scope.backend", "entries", "entries.service", "entries.body", "entries.attributes.*", "suggested"},
}

func sampleDoc() doc {
	d := doc{
		Scope: Scope{Env: "dev", Backend: "local",
			From: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 22, 10, 15, 0, 0, time.UTC)},
		Entries: []entry{
			{Service: "api", Body: "boom", Attrs: map[string]string{"http.route": "/x", "k": "v"}},
			{Service: "worker", Body: "ok"},
		},
		Suggested: Suggest(Command("forge", "obs", "trace", "abc", "dev")),
	}
	d.Stamp(docSchema.Name, nil)
	return d
}

func TestStampTakesTheExitCodeFromTheError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		ok   bool
		code int
	}{
		{"nil", nil, true, 0},
		{"plain", errors.New("bad"), false, 1},
		{"coded 2", coded{2, "cannot tell"}, false, 2},
		{"wrapped coded", fmt.Errorf("ctx: %w", coded{8, "timeout"}), false, 8},
		{"coded 0 is still a failure", coded{0, "odd"}, false, 1},
	}
	for _, c := range cases {
		var h Head
		h.Stamp("s/v1", c.err)
		if h.OK != c.ok || h.ExitCode != c.code || h.Schema != "s/v1" {
			t.Errorf("%s: got %+v", c.name, h)
		}
		if (c.err == nil) != (h.Error == "") {
			t.Errorf("%s: error text %q", c.name, h.Error)
		}
	}
}

func TestGoldenDocument(t *testing.T) {
	var b bytes.Buffer
	if err := docSchema.Write(&b, sampleDoc(), nil); err != nil {
		t.Fatal(err)
	}
	golden(t, "document.golden.json", b.Bytes())
}

func TestGoldenFailure(t *testing.T) {
	var b bytes.Buffer
	f := Failed("forge.obs.logs/v1", coded{2, "the local ClickStack is not running"}, Command("forge", "env", "up", "dev"))
	if err := (Schema{Name: "forge.obs.logs/v1"}).Write(&b, f, nil); err != nil {
		t.Fatal(err)
	}
	golden(t, "failure.golden.json", b.Bytes())
}

func TestFieldsProjectionKeepsHeadAndPageAndOrder(t *testing.T) {
	d := sampleDoc()
	d.Page = Page{Truncated: true, Omitted: 4, NextPageToken: "tok"}
	fields, err := docSchema.ParseFields("entries.service,scope.env")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := docSchema.Write(&b, d, fields); err != nil {
		t.Fatal(err)
	}
	golden(t, "fields.golden.json", b.Bytes())
}

func TestFieldsSelectingAnObjectSelectsItsSubtree(t *testing.T) {
	fields, _ := docSchema.ParseFields("scope,entries.attributes.http.route")
	var b bytes.Buffer
	if err := docSchema.Write(&b, sampleDoc(), fields); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{`"backend": "local"`, `"http.route": "/x"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, `"body"`) || strings.Contains(got, `"k": "v"`) {
		t.Errorf("an unselected field leaked:\n%s", got)
	}
}

func TestParseFieldsRejectsAnUnknownFieldAndListsTheRealOnes(t *testing.T) {
	_, err := docSchema.ParseFields("entries.servce")
	if err == nil {
		t.Fatal("a typo must be an error, not an empty answer")
	}
	for _, want := range []string{"entries.servce", "forge.obs.test/v1", "entries.service", "scope.env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if fs, err := docSchema.ParseFields(" , "); err != nil || len(fs) != 0 {
		t.Errorf("empty value: %v %v", fs, err)
	}
	if _, err := docSchema.ParseFields("entries.attributes.anything.at.all"); err != nil {
		t.Errorf("an open map takes any key: %v", err)
	}
}

func TestHelpListsEveryFieldAndWhatIsAlwaysKept(t *testing.T) {
	h := docSchema.Help()
	for _, f := range docSchema.Fields {
		if !strings.Contains(h, "  "+f+"\n") {
			t.Errorf("help lacks %q:\n%s", f, h)
		}
	}
	if !strings.Contains(h, "next_page_token") || !strings.Contains(h, "forge.obs.test/v1") {
		t.Errorf("help:\n%s", h)
	}
}

func TestSuggestIsBoundedDistinctAndOrdered(t *testing.T) {
	got := Suggest("a", "", "a", "b", " c ", "d", "e")
	if want := []string{"a", "b", "c"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", got, want)
	}
	if len(Suggest()) != 0 {
		t.Error("nothing suggested must stay absent, not an empty placeholder")
	}
}

func TestCommandQuotesWhatTheShellWouldSplit(t *testing.T) {
	got := Command("forge", "obs", "logs", "dev", "service:api AND \"x\"", "it's", "--since=15m", "")
	want := `forge obs logs dev 'service:api AND "x"' 'it'\''s' --since=15m ''`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestBoundAndCap(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	kept, p := Bound(items, 3, "")
	if len(kept) != 3 || p != (Page{Truncated: true, Omitted: 2}) {
		t.Errorf("cap: %v %+v", kept, p)
	}
	kept, p = Bound(items, 10, "")
	if len(kept) != 5 || p != (Page{}) {
		t.Errorf("under the cap: %v %+v", kept, p)
	}
	_, p = Bound(items, 10, "next")
	if !p.Truncated || p.Omitted != 0 || p.NextPageToken != "next" {
		t.Errorf("backend page: %+v", p)
	}
	if kept, p = Bound(items, 0, ""); len(kept) != 5 || p.Truncated {
		t.Errorf("no cap: %v %+v", kept, p)
	}
}

func TestAbsentMeansUnknown(t *testing.T) {
	var b bytes.Buffer
	d := doc{Entries: []entry{}}
	d.Stamp(docSchema.Name, nil)
	if err := docSchema.Write(&b, d, nil); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"null", `"omitted"`, `"next_page_token"`, `"error"`, `"suggested"`} {
		if strings.Contains(b.String(), banned) {
			t.Errorf("document carries %s:\n%s", banned, b.String())
		}
	}
}

func TestStreamEndsWithTheEnvelope(t *testing.T) {
	var b bytes.Buffer
	s := NewStream(&b, docSchema, []string{"service"})
	if err := s.Record(entry{Service: "api", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(entry{Service: "worker", Body: "y"}); err != nil {
		t.Fatal(err)
	}
	if err := s.End(coded{2, "stream lost"}); err != nil {
		t.Fatal(err)
	}
	golden(t, "stream.golden.ndjson", b.Bytes())
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
}

func TestStreamEndOnSuccess(t *testing.T) {
	var b bytes.Buffer
	if err := NewStream(&b, docSchema, nil).End(nil); err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"forge.obs.test/v1","ok":true,"exit_code":0,"end":true}` + "\n"
	if b.String() != want {
		t.Errorf("got %s want %s", b.String(), want)
	}
}

func TestTableAlignsAndFlattensCells(t *testing.T) {
	var b bytes.Buffer
	Table(&b, []string{"TIME", "SERVICE", "BODY"}, [][]string{
		{"10:00:00", "api", "line one\nline two\twith tab"},
		{"10:00:01", "worker-long", strings.Repeat("x", 30)},
	}, 12)
	golden(t, "table.golden.txt", b.Bytes())
}

func TestFlagsRegisterAndEmit(t *testing.T) {
	var f Flags
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	f.Register(fs, 50)
	if err := fs.Parse([]string{"--json", "--fields", "entries.service", "--limit", "7"}); err != nil {
		t.Fatal(err)
	}
	if !f.JSON || f.Limit != 7 {
		t.Fatalf("%+v", f)
	}
	fields, err := f.Parsed(docSchema)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	done, err := f.Emit(&b, docSchema, sampleDoc(), fields)
	if err != nil || !done || !strings.Contains(b.String(), `"service": "api"`) {
		t.Errorf("done=%v err=%v\n%s", done, err, b.String())
	}
	f.JSON = false
	if done, _ := f.Emit(&b, docSchema, sampleDoc(), nil); done {
		t.Error("without --json the caller renders the human form")
	}
	var def Flags
	def.Register(pflag.NewFlagSet("d", pflag.ContinueOnError), 50)
	if def.Limit != 50 {
		t.Errorf("default limit %d", def.Limit)
	}
}
