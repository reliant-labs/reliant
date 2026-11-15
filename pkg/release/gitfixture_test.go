package release

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // git's object id format, not a security use
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Git fixtures for this package's tests: each repository is built ONCE per
// test process, as a template, and every test gets its own copy.
//
// A git subprocess is a process start, and on a loaded machine a start costs
// about a second (measured: 20 `git rev-parse` took 20.6s at load average
// 157 on 32 CPUs). Building every test's repository with its own `git init`
// and `git commit` spent most of a capture test on fixtures. A copy is a few
// small file writes, and git sees the same repository: the objects are
// byte-identical, and the index's stale stat data only makes `git status`
// re-read the (tiny) files.

var fixtures struct {
	mu    sync.Mutex
	root  string
	byKey map[string]*repoTemplate
}

type repoTemplate struct {
	once sync.Once
	dir  string
	meta []string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtures.root != "" {
		_ = os.RemoveAll(fixtures.root)
	}
	os.Exit(code)
}

// templateRepo returns a fresh copy of the repository build makes for key,
// building it on first use, and the metadata build returned with it.
func templateRepo(t *testing.T, key string, build func(dir string) ([]string, error)) (string, []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fixtures.mu.Lock()
	if fixtures.root == "" {
		root, err := os.MkdirTemp("", "forge-release-fixtures-*")
		if err != nil {
			fixtures.mu.Unlock()
			t.Fatal(err)
		}
		fixtures.root = root
		fixtures.byKey = map[string]*repoTemplate{}
	}
	tpl := fixtures.byKey[key]
	if tpl == nil {
		tpl = &repoTemplate{dir: filepath.Join(fixtures.root, key)}
		fixtures.byKey[key] = tpl
	}
	fixtures.mu.Unlock()

	tpl.once.Do(func() {
		if tpl.err = os.Mkdir(tpl.dir, 0o755); tpl.err == nil {
			tpl.meta, tpl.err = build(tpl.dir)
		}
	})
	if tpl.err != nil {
		t.Fatalf("build git fixture %q: %v", key, tpl.err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(tpl.dir)); err != nil {
		t.Fatalf("copy git fixture %q: %v", key, err)
	}
	return dir, tpl.meta
}

// fixtureGit runs git for a template build, with a fixed identity.
func fixtureGit(dir, stdin string, args ...string) (string, error) {
	// The identity as -c flags, not an environment built from os.Environ:
	// pkg/ reads no ambient environment, tests included (internal/pkgguard).
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// initRepo is a checkout of main with one commit ("init", by t) holding
// a.txt = "one\n", and origin set to git@github.com:acme/app.git. It returns
// the checkout and HEAD's tree as git reports it.
func initRepo(t *testing.T) (dir, headTree string) {
	t.Helper()
	dir, meta := templateRepo(t, "init", func(dir string) ([]string, error) {
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
			return nil, err
		}
		for _, args := range [][]string{
			{"init", "-q", "-b", "main"},
			{"remote", "add", "origin", "git@github.com:acme/app.git"},
			{"add", "a.txt"},
			{"commit", "-q", "-m", "init"},
		} {
			if _, err := fixtureGit(dir, "", args...); err != nil {
				return nil, err
			}
		}
		tree, err := fixtureGit(dir, "", "rev-parse", "HEAD^{tree}")
		return []string{tree}, err
	})
	return dir, meta[0]
}

func gitInit(t *testing.T) string {
	t.Helper()
	dir, _ := initRepo(t)
	return dir
}

// gitHistory builds a repository whose main is n commits ("c1" … "cn", by
// alice then bob alternately) in one fast-import, and returns it with the
// shas oldest first.
func gitHistory(t *testing.T, n int) (string, []string) {
	t.Helper()
	return templateRepo(t, fmt.Sprintf("history-%d", n), func(dir string) ([]string, error) {
		var stream strings.Builder
		for i := 1; i <= n; i++ {
			author := "alice"
			if i%2 == 0 {
				author = "bob"
			}
			msg := fmt.Sprintf("c%d\n\nbody line that is not the subject\n", i)
			content := fmt.Sprintf("%d\n", i)
			fmt.Fprintf(&stream, "commit refs/heads/main\nauthor %s <%s@example.com> %d +0000\ncommitter %s <%s@example.com> %d +0000\ndata %d\n%sM 644 inline f.txt\ndata %d\n%s\n",
				author, author, 1700000000+i*60, author, author, 1700000000+i*60, len(msg), msg, len(content), content)
		}
		for _, step := range []struct {
			stdin string
			args  []string
		}{
			{"", []string{"init", "-q", "-b", "main"}},
			{stream.String(), []string{"fast-import", "--quiet"}},
			{"", []string{"reset", "-q", "--hard", "main"}},
		} {
			if _, err := fixtureGit(dir, step.stdin, step.args...); err != nil {
				return nil, err
			}
		}
		out, err := fixtureGit(dir, "", "rev-list", "--reverse", "main")
		if err != nil {
			return nil, err
		}
		shas := strings.Fields(out)
		if len(shas) != n {
			return nil, fmt.Errorf("fast-import wrote %d commits, want %d", len(shas), n)
		}
		return shas, nil
	})
}

// treeHash is git's SHA-1 tree id for a flat directory of regular files,
// computed without git, so a test can state the exact tree a capture must
// report rather than only that it differs from some other one.
func treeHash(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var body bytes.Buffer
	for _, name := range names {
		content := files[name]
		blob := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))) //nolint:gosec // git object id
		fmt.Fprintf(&body, "100644 %s\x00", name)
		body.Write(blob[:])
	}
	sum := sha1.Sum(append([]byte(fmt.Sprintf("tree %d\x00", body.Len())), body.Bytes()...)) //nolint:gosec // git object id
	return hex.EncodeToString(sum[:])
}
