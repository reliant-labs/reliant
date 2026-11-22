package vulnpatch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The Python patchers with the real tools, against a package index this
// test serves itself, so no network is touched: forgetestlib 1.0.0 (pulled
// in by forgetesttop) is locked first, then 1.0.1 (the fix) and 1.1.0 are
// published, and each tool must land on the fix. A tool missing from PATH
// skips its subtest.

// pyIndex is a PEP 503 simple index of wheels built in memory; publish
// makes a version visible.
type pyIndex struct {
	mu     sync.Mutex
	wheels map[string]map[string][]byte // project -> filename -> wheel
}

func newPyIndex(t *testing.T) (*pyIndex, string) {
	ix := &pyIndex{wheels: map[string]map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(ix.serve))
	t.Cleanup(srv.Close)
	return ix, srv.URL + "/simple/"
}

func (ix *pyIndex) publish(t *testing.T, name, version string, requires ...string) {
	t.Helper()
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.wheels[name] == nil {
		ix.wheels[name] = map[string][]byte{}
	}
	ix.wheels[name][fmt.Sprintf("%s-%s-py3-none-any.whl", name, version)] = buildWheel(t, name, version, requires)
}

func (ix *pyIndex) serve(w http.ResponseWriter, r *http.Request) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "simple":
		files := ix.wheels[pyName(parts[1])]
		if files == nil {
			http.NotFound(w, r)
			return
		}
		names := make([]string, 0, len(files))
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!DOCTYPE html><html><body>\n")
		for _, f := range names {
			sum := sha256.Sum256(files[f])
			_, _ = fmt.Fprintf(w, "<a href=\"/files/%s/%s#sha256=%s\">%s</a><br/>\n", parts[1], f, hex.EncodeToString(sum[:]), f)
		}
		_, _ = fmt.Fprint(w, "</body></html>\n")
	case len(parts) == 3 && parts[0] == "files":
		b, ok := ix.wheels[pyName(parts[1])][parts[2]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// buildWheel is a minimal pure-Python wheel with a correct RECORD.
func buildWheel(t *testing.T, name, version string, requires []string) []byte {
	t.Helper()
	info := fmt.Sprintf("%s-%s.dist-info", name, version)
	meta := fmt.Sprintf("Metadata-Version: 2.1\nName: %s\nVersion: %s\n", name, version)
	for _, r := range requires {
		meta += "Requires-Dist: " + r + "\n"
	}
	files := [][2]string{
		{name + "/__init__.py", ""},
		{info + "/METADATA", meta},
		{info + "/WHEEL", "Wheel-Version: 1.0\nGenerator: forge-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n"},
	}
	var record strings.Builder
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		sum := sha256.Sum256([]byte(f[1]))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", f[0], base64.RawURLEncoding.EncodeToString(sum[:]), len(f[1]))
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(f[1]))
	}
	record.WriteString(info + "/RECORD,,\n")
	w, err := zw.Create(info + "/RECORD")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(record.String()))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// realPython is a runner for the real tools, with every cache and config
// in the test's own directories and the local index the only one.
func realPython(t *testing.T, index string) vulnscan.Runner {
	home := t.TempDir()
	env := []string{
		"HOME=" + home, "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"UV_CACHE_DIR=" + filepath.Join(home, "uv"), "UV_PYTHON_DOWNLOADS=never", "UV_DEFAULT_INDEX=" + index,
		"PIP_INDEX_URL=" + index, "PIP_CONFIG_FILE=/dev/null", "PIP_DISABLE_PIP_VERSION_CHECK=1",
		"POETRY_CACHE_DIR=" + filepath.Join(home, "poetry-cache"), "POETRY_CONFIG_DIR=" + filepath.Join(home, "poetry-config"),
		"POETRY_DATA_DIR=" + filepath.Join(home, "poetry-data"), "POETRY_VIRTUALENVS_IN_PROJECT=true", "POETRY_NO_INTERACTION=1",
	}
	return func(ctx context.Context, c vulnscan.Command) error {
		c.Env = append(os.Environ(), env...)
		return vulnscan.Exec(ctx, c)
	}
}

func runIn(t *testing.T, run vulnscan.Runner, dir string, argv ...string) {
	t.Helper()
	var out bytes.Buffer
	if err := run(context.Background(), vulnscan.Command{Path: argv[0], Args: argv[1:], Dir: dir, Stdout: &out, Stderr: &out}); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out.String())
	}
}

// patchReal plans, applies and documents findings against root with run.
func patchReal(t *testing.T, root string, run vulnscan.Runner, findings ...vulnscan.Finding) Document {
	t.Helper()
	p, err := NewPlan(root, findings, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	doc := p.Document("", "", false)
	if doc.ExitCode != ExitOK {
		t.Fatalf("exit %d: findings %+v bumps %+v", doc.ExitCode, doc.Findings, doc.Bumps)
	}
	return doc
}

func lockedVersion(t *testing.T, root, lock, name string) string {
	t.Helper()
	m, err := readPyLock(root, filepath.Join(root, lock))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(m.pins[name], ",")
}

func TestRealPythonBumps(t *testing.T) {
	testkit.Slow(t, "execs uv, pip-compile and poetry against a package index served by the test")
	tool := func(t *testing.T, name string) {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not on PATH", name)
		}
	}
	setup := func(t *testing.T) (*pyIndex, string, vulnscan.Runner) {
		ix, index := newPyIndex(t)
		ix.publish(t, "forgetestlib", "1.0.0")
		ix.publish(t, "forgetesttop", "1.0.0", "forgetestlib>=1.0")
		return ix, index, realPython(t, index)
	}
	fixes := func(t *testing.T, ix *pyIndex) {
		ix.publish(t, "forgetestlib", "1.0.1")
		ix.publish(t, "forgetestlib", "1.1.0")
		ix.publish(t, "forgetesttop", "1.0.1", "forgetestlib>=1.0")
	}
	lib := pyFinding("CVE-1", "forgetestlib", "1.0.0", "1.0.1")

	t.Run("uv", func(t *testing.T) {
		tool(t, "uv")
		ix, index, run := setup(t)
		root := writeTree(t, map[string]string{"pyproject.toml": `[project]
name = "app"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = ["forgetesttop==1.0.0"]

[[tool.uv.index]]
name = "local"
url = "` + index + `"
default = true
`})
		runIn(t, run, root, "uv", "lock")
		fixes(t, ix)
		doc := patchReal(t, root, run, lib)
		// 1.1.0 is published too: only the specifier in --upgrade-package
		// keeps uv on the fix, so this proves uv reads it as a constraint.
		if got := lockedVersion(t, root, "uv.lock", "forgetestlib"); got != "1.0.1" {
			t.Errorf("uv.lock locks forgetestlib %s, want exactly the fix 1.0.1", got)
		}
		if cmd := strings.Join(doc.Bumps[0].Commands[0], " "); cmd != "uv lock --upgrade-package forgetestlib==1.0.1" {
			t.Errorf("command = %s", cmd)
		}
	})

	t.Run("pip-compile", func(t *testing.T) {
		tool(t, "pip-compile")
		ix, _, run := setup(t)
		root := writeTree(t, map[string]string{"requirements.in": "forgetesttop==1.0.0\n"})
		runIn(t, run, root, "pip-compile", "--generate-hashes", "--no-emit-index-url", "requirements.in")
		fixes(t, ix)
		f := pyFinding("CVE-2", "forgetesttop", "1.0.0", "1.0.1")
		patchReal(t, root, run, lib, f)
		txt := readFile(t, root, "requirements.txt")
		if !strings.Contains(txt, "forgetestlib==1.0.1 \\\n    --hash=sha256:") || !strings.Contains(txt, "forgetesttop==1.0.1 \\\n") {
			t.Errorf("requirements.txt:\n%s", txt)
		}
		if got := readFile(t, root, "requirements.in"); got != "forgetesttop==1.0.1\n" {
			t.Errorf("requirements.in = %q", got)
		}
	})

	t.Run("uv pip compile", func(t *testing.T) {
		tool(t, "uv")
		ix, _, run := setup(t)
		root := writeTree(t, map[string]string{"requirements/base.in": "forgetesttop\n"})
		runIn(t, run, root, "uv", "pip", "compile", "requirements/base.in", "-o", "requirements/base.txt")
		fixes(t, ix)
		doc := patchReal(t, root, run, lib)
		if txt := readFile(t, root, "requirements/base.txt"); !strings.Contains(txt, "forgetestlib==1.0.1\n") || !strings.Contains(txt, "forgetesttop==1.0.0\n") {
			t.Errorf("requirements/base.txt:\n%s", txt)
		}
		if cmd := strings.Join(doc.Bumps[0].Commands[0], " "); cmd != "uv pip compile requirements/base.in --quiet -o requirements/base.txt --upgrade-package forgetestlib==1.0.1" {
			t.Errorf("command = %s", cmd)
		}
	})

	t.Run("poetry", func(t *testing.T) {
		tool(t, "poetry")
		ix, index, run := setup(t)
		root := writeTree(t, map[string]string{"pyproject.toml": `[project]
name = "app"
version = "0.1.0"
requires-python = ">=3.10"
dependencies = ["forgetesttop (==1.0.0)"]

[tool.poetry]
package-mode = false

[[tool.poetry.source]]
name = "local"
url = "` + index + `"
priority = "primary"
`})
		runIn(t, run, root, "poetry", "lock")
		fixes(t, ix)
		// forgetesttop is pinned exactly (poetry add); forgetestlib is
		// transitive (poetry update).
		doc := patchReal(t, root, run, lib, pyFinding("CVE-2", "forgetesttop", "1.0.0", "1.0.1"))
		if got := lockedVersion(t, root, "poetry.lock", "forgetesttop"); got != "1.0.1" {
			t.Errorf("poetry.lock locks forgetesttop %s, want 1.0.1", got)
		}
		if got := lockedVersion(t, root, "poetry.lock", "forgetestlib"); comparePyVersions(got, "1.0.1") < 0 {
			t.Errorf("poetry.lock locks forgetestlib %s, want 1.0.1 or later", got)
		}
		if cmds := doc.Bumps[0].Commands; len(cmds) != 2 || strings.Join(cmds[0], " ") != "poetry add forgetesttop@^1.0.1" {
			t.Errorf("commands = %q", cmds)
		}
	})
}
