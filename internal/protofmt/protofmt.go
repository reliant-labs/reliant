// Package protofmt keeps every .proto forge writes byte-identical to what
// `buf format` makes of it.
//
// A scaffolded project gates its protos on `buf format -d --exit-code` (the
// pre-commit hook forge writes), so a proto forge edits must already be buf's
// output or the user's first commit is refused over text forge wrote. Forge's
// emitters write canonical shapes for what they author themselves, but some
// of what they write is the project's own text carried over — the
// `(buf.validate.field)` rules a Create<Entity>Request repeats from its
// entity, a field's braced aggregate, an import list the project ordered — and
// matching buf for those would mean re-implementing buf's formatter and
// chasing its every release. So the formatter IS buf: the same binary
// `forge generate` runs, over exactly the files forge changed.
//
// A command that writes protos opens a session (Begin) before its first
// write. The session remembers every proto under proto/ as it stood. Settling
// it (Settle, and the end func Begin returns) runs buf format over each proto
// that changed since and writes buf's output back — but only where forge's
// edit is all that stands between the file and buf's output: a file that was
// not buf-format clean before the command is the author's formatting, and
// forge leaves it as the author left it (the edit still lands; the
// pre-existing drift stays theirs to settle). A file forge created is always
// formatted. Forge's vendored copies (forge/v1/forge.proto,
// buf/validate/validate.proto) are never rewritten: their bytes are pinned to
// the binary's embedded copy, and `forge lint --vendored-protos` holds them
// there.
//
// The generate pipeline settles any open session for its project before it
// fingerprints the tree, so a scaffold's own generate run reads settled protos
// and the generate cache never records the pre-format bytes.
package protofmt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// protoDir is the tree forge writes protos into, relative to the project root.
const protoDir = "proto"

// session is one command's record of the protos it may edit.
type session struct {
	root string
	warn io.Writer
	// before is each proto's content (slash-separated, project-relative path)
	// as of Begin, or as the last Settle left it.
	before map[string][]byte
	// warned keeps an unavailable buf to one warning per session.
	warned bool
}

var (
	mu     sync.Mutex
	active = map[string]*session{}
)

// Begin opens a session for the project at root and returns the func that
// settles and closes it. Call it before the command's first proto write.
// warn receives the one-line reports (what was formatted, a buf that could not
// run); nil discards them.
//
// A session already open for root (a scaffold noun that runs `project new`'s
// helpers, say) is the outer command's: the nested Begin returns a no-op, so
// the outer end settles everything once.
func Begin(root string, warn io.Writer) (end func()) {
	abs := absRoot(root)
	if warn == nil {
		warn = io.Discard
	}
	mu.Lock()
	defer mu.Unlock()
	if _, open := active[abs]; open {
		return func() {}
	}
	active[abs] = &session{root: abs, warn: warn, before: readProtos(abs)}
	return func() {
		_, _ = Settle(context.Background(), abs)
		mu.Lock()
		delete(active, abs)
		mu.Unlock()
	}
}

// Settle formats, with buf, every proto under root that changed since the open
// session began (or last settled) and was buf-format clean before it changed,
// and returns the project-relative paths it rewrote. Without an open session
// for root it does nothing: only a command that declared it writes protos has
// edits to settle. A buf that cannot run is reported once on the session's
// warn writer and is not an error — the scaffold stands, and `forge lint
// --proto-format` reports what is left.
func Settle(ctx context.Context, root string) ([]string, error) {
	abs := absRoot(root)
	mu.Lock()
	s := active[abs]
	mu.Unlock()
	if s == nil {
		return nil, nil
	}
	return s.settle(ctx)
}

func (s *session) settle(ctx context.Context) ([]string, error) {
	now := readProtos(s.root)
	var edited []string
	for rel, content := range now {
		if old, ok := s.before[rel]; ok && bytes.Equal(old, content) {
			continue
		}
		edited = append(edited, rel)
	}
	if len(edited) == 0 {
		return nil, nil
	}
	sort.Strings(edited)
	if !fileExists(filepath.Join(s.root, "buf.yaml")) {
		// No buf module: nothing in this project runs buf format, so there
		// is no format to converge on.
		s.before = now
		return nil, nil
	}
	bin, err := resolveBin(ctx, s.root)
	if err != nil {
		s.warnUnavailable(err)
		return nil, err
	}
	var changed []string
	var errs []error
	for _, rel := range edited {
		content := now[rel]
		if old, existed := s.before[rel]; existed {
			canonical, ferr := Format(ctx, bin, old)
			if ferr != nil || !bytes.Equal(canonical, old) {
				// The author's formatting, not forge's to settle.
				s.before[rel] = content
				continue
			}
		}
		formatted, ferr := Format(ctx, bin, content)
		if ferr != nil {
			// Unparseable: buf generate reports it with a real error.
			errs = append(errs, fmt.Errorf("%s: %w", rel, ferr))
			s.before[rel] = content
			continue
		}
		if !bytes.Equal(formatted, content) {
			abs := filepath.Join(s.root, filepath.FromSlash(rel))
			mode := fs.FileMode(0o644)
			if info, statErr := os.Stat(abs); statErr == nil {
				mode = info.Mode().Perm()
			}
			if werr := atomicfile.WriteFile(abs, formatted, mode); werr != nil {
				errs = append(errs, werr)
				continue
			}
			changed = append(changed, rel)
		}
		s.before[rel] = formatted
	}
	// A file that disappeared is simply no longer tracked.
	for rel := range s.before {
		if _, ok := now[rel]; !ok {
			delete(s.before, rel)
		}
	}
	if len(changed) > 0 {
		_, _ = fmt.Fprintf(s.warn, "🧹 buf format: settled %d proto file(s) forge edited (%s)\n", len(changed), strings.Join(changed, ", "))
	}
	return changed, errors.Join(errs...)
}

func (s *session) warnUnavailable(err error) {
	if s.warned {
		return
	}
	s.warned = true
	_, _ = fmt.Fprintf(s.warn, "⚠️  could not run buf format over the protos forge edited (%v); run `buf format -w` from the project root before you commit\n", err)
}

// Format returns src as the buf at bin formats it. buf formats a single file
// named on its command line without a module around it, so src is written to
// a scratch directory of its own: nothing in the project is read or written.
func Format(ctx context.Context, bin string, src []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "forge-protofmt-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in := filepath.Join(dir, "in.proto")
	if err := atomicfile.WriteFile(in, src, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "format", in)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("buf format: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// resolveBin is the buf this project formats with: the one `forge generate`
// runs (cli's pinBuf). go.mod's pin when the project declares one; otherwise
// the buf on PATH, else forge's pin built into the shared tool cache. A seam
// for tests.
var resolveBin = func(ctx context.Context, root string) (string, error) {
	if !toolpin.BufFor(root).Declared() {
		if p, err := exec.LookPath("buf"); err == nil {
			return p, nil
		}
	}
	dir, err := cigate.NewResolver(root, io.Discard).Ensure(ctx, cigate.BufTool(root))
	if err != nil {
		return "", err
	}
	if dir == "" {
		return exec.LookPath("buf")
	}
	bin := filepath.Join(dir, "buf")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin, nil
}

// readProtos reads every .proto under root/proto that a session may settle,
// keyed by slash-separated project-relative path. Forge's vendored copies are
// left out (see the package doc).
func readProtos(root string) map[string][]byte {
	out := map[string][]byte{}
	_ = filepath.WalkDir(filepath.Join(root, protoDir), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is simply not tracked
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // unreachable under a walk rooted inside root
		}
		rel = filepath.ToSlash(rel)
		if assets.IsVendoredProtoRelPath(rel) {
			return nil
		}
		b, readErr := os.ReadFile(path) //nolint:gosec // a proto under the project's own tree
		if readErr != nil {
			return nil //nolint:nilerr // unreadable: not tracked
		}
		out[rel] = b
		return nil
	})
	return out
}

func absRoot(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
