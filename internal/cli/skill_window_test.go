package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSkillLoadSection(t *testing.T) {
	t.Parallel()
	body := []byte("---\nname: fixture\n---\n# Title\n## Setup advanced\nadvanced\n## Setup ##\nfirst\n```sh\n## not a heading\n```\n~~~\n# Also not one\n~~~\n### Choice\nsecond\n## Cleanup\nlast")
	for _, tt := range []struct{ query, want, wantErr string }{
		{query: "SETUP", want: "## Setup ##\nfirst\n```sh\n## not a heading\n```\n~~~\n# Also not one\n~~~\n### Choice\nsecond\n"},
		{query: "adv", want: "## Setup advanced\nadvanced\n"},
		{query: "cleanup", want: "## Cleanup\nlast"},
		{query: "set", wantErr: "ambiguous: Setup advanced (line 5), Setup (line 7)"},
		{query: "not a heading", wantErr: "available headings: Title, Setup advanced, Setup, Choice, Cleanup"},
	} {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			got, err := (skillLoadWindow{section: tt.query}).selectFromSkill(body)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %q, %v; want error containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestSkillLoadRegex(t *testing.T) {
	t.Parallel()
	body := []byte("# Title\r\nresult\r\n## One\r\nRESULT\r\nnext\r\nspacer\r\nspacer\r\nbefore\r\nresult\r\n## Two\r\nresult")
	w := skillLoadWindow{section: "one", pattern: "^result$", ignoreCase: true, contextLines: 1}
	got, err := w.selectFromSkill(body)
	want := "3-## One\n4:RESULT\n5-next\n--\n8-before\n9:result\n"
	if err != nil || string(got) != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	for _, tt := range []struct {
		window skillLoadWindow
		want   string
	}{
		{skillLoadWindow{pattern: "["}, "invalid --regex"},
		{skillLoadWindow{pattern: "missing"}, "no lines matched"},
		{skillLoadWindow{pattern: "result", contextLines: -1}, "--context must be non-negative"},
	} {
		if _, err := tt.window.selectFromSkill(body); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v: got %v, want error containing %q", tt.window, err, tt.want)
		}
	}
}

func TestSkillLoadWindowFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, _ := findProjectRoot()
	body, err := RenderSkillContentAt(root, "api", SkillAudienceAll, Name())
	if err != nil {
		t.Fatal(err)
	}
	load := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		cmd := newSkillLoadCmd()
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"api"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	if got := load(); !bytes.Equal(got, body) {
		t.Fatal("load without filters must print the renderer's exact bytes")
	}
	if got := string(load("--section", "Domain sentinels", "--regex", "errnotfound", "-i", "--context", "1")); !strings.Contains(got, ":| `ErrNotFound`") || strings.Contains(got, "## Validation helpers") {
		t.Fatalf("flags didn't narrow to the domain-sentinels section: %q", got)
	}
}
