package component

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// captureStdout runs fn with os.Stdout redirected: list, search and install
// print with fmt.Printf. Tests using it must not run in parallel.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func execGroup(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	cmd := newCmd(&factory.Factory{})
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	stdout = captureStdout(t, func() { err = cmd.Execute() })
	return stdout, err
}

func TestGroup_RegistersTheThreeSubcommands(t *testing.T) {
	cmd := newCmd(&factory.Factory{})
	var got []string
	for _, c := range cmd.Commands() {
		got = append(got, c.Name())
	}
	for _, want := range []string{"list", "search", "install"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("subcommand %q missing from %v", want, got)
		}
	}
}

func TestGroup_UnknownSubcommandIsRejected(t *testing.T) {
	_, err := execGroup(t, "instal")
	if err == nil || !strings.Contains(err.Error(), `unknown command "instal"`) || !strings.Contains(err.Error(), "install") {
		t.Fatalf("err = %v, want an unknown-command error suggesting install", err)
	}
}

func TestList(t *testing.T) {
	t.Run("without a filter lists every category", func(t *testing.T) {
		out, err := execGroup(t, "list")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"## CHARTS", "## LAYOUTS", "quadrant_chart"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
	})

	t.Run("--category narrows to one category", func(t *testing.T) {
		out, err := execGroup(t, "list", "--category", "charts")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "## CHARTS") || !strings.Contains(out, "quadrant_chart") {
			t.Errorf("charts missing:\n%s", out)
		}
		if strings.Contains(out, "## LAYOUTS") {
			t.Errorf("--category charts leaked another category:\n%s", out)
		}
	})

	t.Run("a category with no components lists none", func(t *testing.T) {
		out, err := execGroup(t, "list", "-c", "no-such-category")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "## ") {
			t.Errorf("an unknown category must list no sections:\n%s", out)
		}
	})
}

func TestSearch(t *testing.T) {
	t.Run("no match says so and succeeds", func(t *testing.T) {
		out, err := execGroup(t, "search", "zzzqq")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `No components found matching "zzzqq"`) {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("a full match lists components without the partial-match warning", func(t *testing.T) {
		out, err := execGroup(t, "search", "funnel")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "funnel_chart") {
			t.Errorf("funnel_chart missing:\n%s", out)
		}
		if strings.Contains(out, "No component matches all") {
			t.Errorf("a full match must not warn about partial terms:\n%s", out)
		}
	})

	t.Run("a partial match says how much of the query matched", func(t *testing.T) {
		out, err := execGroup(t, "search", "funnel", "zzzqq")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `No component matches all 2 terms in "funnel zzzqq" — showing the best matches (1 of 2 terms).`) {
			t.Errorf("partial-match notice missing:\n%s", out)
		}
		if !strings.Contains(out, "funnel_chart") {
			t.Errorf("best matches missing:\n%s", out)
		}
	})

	t.Run("a query is required", func(t *testing.T) {
		if _, err := execGroup(t, "search"); err == nil {
			t.Fatal("search with no query must fail")
		}
	})
}

func TestInstall(t *testing.T) {
	t.Run("--dir naming a frontend installs into its src/components/ui", func(t *testing.T) {
		fe := t.TempDir()
		writeFrontend(t, fe)
		var out string
		var err error
		out, err = execGroup(t, "install", "--dir", fe, "button")
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(fe, "src", "components", "ui", "button.tsx")
		if _, serr := os.Stat(dest); serr != nil {
			t.Fatalf("button.tsx not written under components/ui: %v", serr)
		}
		if !strings.Contains(out, "Installed button -> "+dest) {
			t.Errorf("output lacks the install line:\n%s", out)
		}
	})

	t.Run("with no --dir the nearest frontend of the project is detected", func(t *testing.T) {
		root := writeProject(t, "web")
		t.Chdir(root)
		if _, err := execGroup(t, "install", "badge"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "frontends", "web", "src", "components", "ui", "badge.tsx")); err != nil {
			t.Fatalf("badge.tsx not installed in the detected frontend: %v", err)
		}
	})

	t.Run("with no --dir and no frontend the error names --dir and nothing is written", func(t *testing.T) {
		bare := t.TempDir()
		t.Chdir(bare)
		_, err := execGroup(t, "install", "button")
		if err == nil || !strings.Contains(err.Error(), "--dir") {
			t.Fatalf("err = %v, want an error naming --dir", err)
		}
		if entries, _ := os.ReadDir(bare); len(entries) != 0 {
			t.Errorf("a failed detection wrote %d entries", len(entries))
		}
	})

	t.Run("an unknown component is refused with near-miss suggestions", func(t *testing.T) {
		dir := t.TempDir()
		_, err := execGroup(t, "install", "--dir", dir, "butto")
		if err == nil || !strings.Contains(err.Error(), `component "butto" not found. Did you mean: button`) {
			t.Fatalf("err = %v", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("nothing should be installed for an unknown component, found %d files", len(entries))
		}
	})

	t.Run("an unknown component with nothing similar gets the plain error", func(t *testing.T) {
		_, err := execGroup(t, "install", "--dir", t.TempDir(), "qqqqqqqqqqqqqqqq")
		if err == nil || err.Error() != `component "qqqqqqqqqqqqqqqq" not found` {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a case-colliding file already there is refused, not overwritten", func(t *testing.T) {
		dir := t.TempDir()
		existing := filepath.Join(dir, "Button.tsx")
		if err := os.WriteFile(existing, []byte("// mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := execGroup(t, "install", "--dir", dir, "button")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "button.tsx") {
			t.Fatalf("err = %v, want a case-collision error naming the file", err)
		}
		if got, _ := os.ReadFile(existing); !bytes.Equal(got, []byte("// mine\n")) {
			t.Errorf("the existing file was overwritten: %q", got)
		}
	})

	t.Run("an uncreatable target directory is reported", func(t *testing.T) {
		parent := t.TempDir()
		blocker := filepath.Join(parent, "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := execGroup(t, "install", "--dir", filepath.Join(blocker, "sub"), "button")
		if err == nil || !strings.Contains(err.Error(), "create directory") {
			t.Fatalf("err = %v, want a 'create directory' error", err)
		}
	})

	t.Run("at least one component name is required", func(t *testing.T) {
		if _, err := execGroup(t, "install", "--dir", t.TempDir()); err == nil {
			t.Fatal("install with no names must fail")
		}
	})
}

func TestWithin(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false}, // a sibling sharing a name prefix is not inside
		{"/a", "/a/b", false},
		{"/x/y", "/a/b", false},
		{"rel/x", "/a/b", false}, // no relative path between a relative and an absolute path
	}
	for _, tc := range tests {
		if got := within(tc.path, tc.dir); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}
