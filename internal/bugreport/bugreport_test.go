package bugreport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPayloadWireBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit string
	}{
		{"ascii", "x "},
		{"unicode", "界😀"},
		{"json escapes", "<\x00\"\\"},
		{"invalid UTF-8", "\xff界"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat(tc.unit, MaxTextBytes)
			r := Report{Severity: "high", Title: text, Summary: text, Expected: text, Actual: text, Evidence: text}
			p := BuildPayload("id", text, r, Context{
				Args: []string{"forge", text}, ForgeVersion: text,
				ProjectForgeVersion: text, Harness: text,
			})
			encoded, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var wire Payload
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []struct {
				name string
				text string
				max  int
			}{
				{"title", wire.Title, MaxTitleBytes},
				{"summary", wire.Summary, MaxTextBytes},
				{"expected", wire.Expected, MaxTextBytes},
				{"actual", wire.Actual, MaxTextBytes},
				{"evidence", wire.Evidence, MaxTextBytes},
				{"command line", wire.Client.CommandLine, maxCommandLineBytes},
				{"forge version", wire.Client.ForgeVersion, maxVersionBytes},
				{"project version", wire.Client.ProjectForgeVersion, maxVersionBytes},
				{"harness", wire.Client.Harness, maxHarnessBytes},
				{"install ID", wire.Client.InstallID, maxInstallIDBytes},
			} {
				if !utf8.ValidString(field.text) || len(field.text) > field.max || utf8.RuneCountInString(field.text) > field.max || !strings.HasSuffix(field.text, " [truncated]") {
					t.Errorf("%s: bytes=%d runes=%d, want valid UTF-8 and marker within %d", field.name, len(field.text), utf8.RuneCountInString(field.text), field.max)
				}
				if tc.name == "ascii" && len(field.text) != field.max {
					t.Errorf("%s: ASCII length=%d, want exact boundary %d", field.name, len(field.text), field.max)
				}
			}
			// JSON escaping can expand bytes; the actual request must still fit
			// the ingest's separate 512 KiB body cap, not just field limits.
			if len(encoded) > 512<<10 {
				t.Errorf("encoded payload %d bytes exceeds ingest body cap", len(encoded))
			}
		})
	}
}

func TestBuildPayloadRedactsHostileContext(t *testing.T) {
	hostile := "password=hunter2 /home/private-person/project " + strings.Repeat("界😀", 100)
	p := BuildPayload("id", hostile, validReport(), Context{
		ForgeVersion: hostile, ProjectForgeVersion: hostile, Harness: hostile,
	})
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hunter2") || strings.Contains(string(encoded), "private-person") {
		t.Fatal("repository-controlled context leaked a credential or personal path")
	}
	for name, value := range map[string]string{
		"forge version": p.Client.ForgeVersion, "project version": p.Client.ProjectForgeVersion,
		"harness": p.Client.Harness, "install ID": p.Client.InstallID,
	} {
		if !strings.Contains(value, "[redacted]") || !strings.Contains(value, "~/project") || !utf8.ValidString(value) {
			t.Errorf("%s was not redacted and UTF-8 bounded: %q", name, value)
		}
	}
}

func validReport() Report {
	return Report{Severity: "high", Title: "forge generate drops a field", Actual: "the field is missing"}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Report)
		want string
	}{
		{"valid", func(*Report) {}, ""},
		{"severity case-insensitive", func(r *Report) { r.Severity = " HIGH " }, ""},
		{"bad severity", func(r *Report) { r.Severity = "urgent" }, "severity must be one of"},
		{"no title", func(r *Report) { r.Title = "  " }, "title is required"},
		{"multi-line title", func(r *Report) { r.Title = "a\nb" }, "title must be one line"},
		{"no description", func(r *Report) { r.Actual = "" }, "describe the defect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validReport()
			tc.edit(&r)
			err := r.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCommandLine(t *testing.T) {
	got := CommandLine([]string{
		"/home/alice/go/bin/forge", "report-bug", "--severity", "high",
		"--title", "secret stuff", "--evidence=more stuff",
		"--token", "abc", "--api-key=xyz",
		"/home/alice/proj", "postgres://u:p@h/db",
	})
	want := "forge report-bug --severity high --title … --evidence=… --token [redacted] --api-key=[redacted] ~/proj postgres://[redacted]@h/db"
	if got != want {
		t.Errorf("CommandLine =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildPayloadRedactsAndBounds(t *testing.T) {
	r := validReport()
	r.Evidence = "ran with ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijkl in /home/bob/x\n" + strings.Repeat("x ", MaxTextBytes)
	p := BuildPayload("id", "install", r, Context{ForgeVersion: "v1", Harness: "claude", Args: []string{"forge"}})
	if strings.Contains(p.Evidence, "sk-ant") || strings.Contains(p.Evidence, "/home/bob") {
		t.Errorf("evidence not redacted: %.120q", p.Evidence)
	}
	if !strings.HasSuffix(p.Evidence, " [truncated]") {
		t.Errorf("evidence not bounded")
	}
	if p.Client.OS == "" || p.Client.Arch == "" || p.Client.GoVersion == "" || p.Client.ForgeVersion != "v1" || p.Client.Harness != "claude" || p.Client.InstallID != "install" {
		t.Errorf("client context = %+v", p.Client)
	}
}

func TestFileDeduplicatesWithinWindow(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var sent []Payload
	f := &Filer{
		State: NewState(t.TempDir()),
		Now:   func() time.Time { return now },
		send:  func(_ context.Context, p Payload) error { sent = append(sent, p); return nil },
	}
	first, err := f.File(context.Background(), validReport(), Context{})
	if err != nil || first.Duplicate {
		t.Fatalf("first File = %+v, %v", first, err)
	}
	// The same defect in other words of case, punctuation and numbers.
	again := validReport()
	again.Title = "Forge generate DROPS a field (42)!"
	now = now.Add(29 * time.Minute)
	second, err := f.File(context.Background(), again, Context{})
	if err != nil || !second.Duplicate || second.ReportID != first.ReportID {
		t.Fatalf("repeat File = %+v, %v; want duplicate of %s", second, err, first.ReportID)
	}
	now = now.Add(2 * time.Minute)
	third, err := f.File(context.Background(), again, Context{})
	if err != nil || third.Duplicate || third.ReportID == first.ReportID {
		t.Fatalf("File after the window = %+v, %v; want a new report", third, err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d reports, want 2", len(sent))
	}
	if sent[0].Client.InstallID == "" || sent[0].Client.InstallID != sent[1].Client.InstallID {
		t.Errorf("install ids %q, %q: want one stable id", sent[0].Client.InstallID, sent[1].Client.InstallID)
	}
}

func TestFileRefusedIsNotRecorded(t *testing.T) {
	fail := true
	f := &Filer{
		State: NewState(t.TempDir()),
		send: func(context.Context, Payload) error {
			if fail {
				return errors.New("unavailable")
			}
			return nil
		},
	}
	if _, err := f.File(context.Background(), validReport(), Context{}); err == nil {
		t.Fatal("File with a failing send succeeded")
	}
	fail = false
	res, err := f.File(context.Background(), validReport(), Context{})
	if err != nil || res.Duplicate {
		t.Fatalf("retry File = %+v, %v; want a fresh send", res, err)
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestFileWire pins the request the ingest receives: the procedure path, the
// protojson field names, and an empty response read as success.
func TestFileWire(t *testing.T) {
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	f := &Filer{Endpoint: srv.URL, Token: "tok", State: NewState(t.TempDir())}
	res, err := f.File(context.Background(), validReport(), Context{ForgeVersion: "v0.1.48", Args: []string{"forge", "report-bug"}})
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if gotPath != "/"+Procedure || gotAuth != "Bearer tok" {
		t.Errorf("request %s auth %q", gotPath, gotAuth)
	}
	if !uuidRE.MatchString(res.ReportID) || body["reportId"] != res.ReportID {
		t.Errorf("report id %q, body reportId %v", res.ReportID, body["reportId"])
	}
	client, _ := body["client"].(map[string]any)
	if body["severity"] != "high" || body["title"] != "forge generate drops a field" || client["forgeVersion"] != "v0.1.48" || client["commandLine"] != "forge report-bug" {
		t.Errorf("body = %v", body)
	}
}

func TestFileRateLimitedSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"resource_exhausted","message":"rate limited"}`))
	}))
	defer srv.Close()
	f := &Filer{Endpoint: srv.URL, State: NewState(t.TempDir())}
	_, err := f.File(context.Background(), validReport(), Context{})
	if err == nil || !strings.Contains(err.Error(), "too many reports") {
		t.Fatalf("File = %v, want a rate-limit message", err)
	}
}

func TestFingerprint(t *testing.T) {
	if a, b := Fingerprint("Lint fails on port 8080"), Fingerprint("lint FAILS on port 9090."); a != b {
		t.Errorf("fingerprints differ: %q vs %q", a, b)
	}
	if got := Fingerprint("123 456"); got != "untitled" {
		t.Errorf("Fingerprint(digits) = %q", got)
	}
}
