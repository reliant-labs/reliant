// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/forgepin"
)

// The guide a pinned forge renders describes the project as it is: which of
// forge's pieces it uses and what its own commands are. Its cache key must
// move whenever any of that can, and nothing the forge reports says what it
// read (`forge project memory` has no input list), so the inputs are named
// here — a superset of what any forge reads, since an extra input costs only
// a re-render and a missing one serves a stale guide for the life of the pin.
//
// The key is computed on every config poll of every project, so every input
// is cheap: a handful of small files read whole, a few directory listings, and
// the proto service declarations (proto/services is a few small files).

// memoryContentInputs are read whole: forge.yaml (name, `envs:`,
// `monitoring:`, `database.migrations`), and the files the project's own
// test / lint / build commands are read from.
var memoryContentInputs = []string{
	"forge.yaml",
	"package.json",
	"pnpm-workspace.yaml",
	"buf.yaml",
	"Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml",
	"GNUmakefile", "makefile", "Makefile",
}

// memoryListedDirs are listed by name (a glob is listed per match): the root
// (go.mod, lockfiles, a frontend's or a migration tool's marker files),
// deploy/kcl and each env directory in it (which hold a main.k, workloads.k),
// the binaries under cmd, the frontends, the protos and the migration tools'
// directories.
var memoryListedDirs = []string{
	".",
	"deploy", "deploy/kcl", "deploy/kcl/*",
	"cmd", "cmd/*",
	"frontends", "frontends/*",
	"proto",
	"prisma", "db", "db/migrate", "db/migrations",
}

// memoryPresenceInputs count only by existing: a forge service's own
// sources. Their parents are not listed, so a new package beside them does
// not re-render the guide.
var memoryPresenceInputs = []string{"pkg/app", "internal/handlers"}

// memoryStatInputs change the guide through their content but are re-read by
// the forge only when they move, so their size and mtime stand for it.
var memoryStatInputs = []string{"frontends/*/package.json"}

// memoryProtoDir holds the service protos; a `// forge:unmanaged` in the
// comment block above a service changes the guide.
const memoryProtoDir = "proto/services"

// memoryFingerprint names what, besides the forge, the guide is rendered
// from (see memoryContentInputs and its siblings).
func memoryFingerprint(root string) string {
	var parts [][]byte
	for _, name := range memoryContentInputs {
		b, _ := os.ReadFile(filepath.Join(root, name)) // absent reads as nil
		parts = append(parts, []byte(name), b)
	}
	listed := memoryListedDirs
	if dir := declaredMigrationsDir(root); dir != "" {
		listed = append(append([]string(nil), listed...), dir)
	}
	for _, pattern := range listed {
		for _, dir := range globUnder(root, pattern) {
			parts = append(parts, []byte(dir), dirNames(filepath.Join(root, dir)))
		}
	}
	for _, rel := range memoryPresenceInputs {
		_, err := os.Stat(filepath.Join(root, rel))
		parts = append(parts, []byte(rel), []byte(strconv.FormatBool(err == nil)))
	}
	for _, pattern := range memoryStatInputs {
		for _, rel := range globUnder(root, pattern) {
			parts = append(parts, []byte(rel), statStamp(filepath.Join(root, rel)))
		}
	}
	parts = append(parts, protoServiceDecls(filepath.Join(root, memoryProtoDir)))
	return forgepin.Fingerprint(parts...)
}

// globUnder is pattern's matches under root, relative to it and sorted; a
// pattern with no wildcard is itself.
func globUnder(root, pattern string) []string {
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{pattern}
	}
	matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if rel, err := filepath.Rel(root, m); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

// dirNames is dir's entry names, a directory's marked with a trailing slash;
// nil when dir is absent or not a directory.
func dirNames(dir string) []byte {
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	for _, e := range entries {
		b.WriteString(e.Name())
		if e.IsDir() {
			b.WriteByte('/')
		}
		b.WriteByte(0)
	}
	return b.Bytes()
}

// statStamp is path's size and mtime; nil when it is absent.
func statStamp(path string) []byte {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return []byte(stampOf(info))
}

func stampOf(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 10) + "/" + strconv.FormatInt(info.Size(), 10)
}

// declaredMigrationsDir is forge.yaml's `database.migrations.dir` (or
// `database.migrations_dir`): a forge that cannot name an adopted app's
// migration tool from forge.yaml reads it off that directory.
func declaredMigrationsDir(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Database struct {
			MigrationsDir string `yaml:"migrations_dir"`
			Migrations    struct {
				Dir string `yaml:"dir"`
			} `yaml:"migrations"`
		} `yaml:"database"`
	}
	_ = yaml.Unmarshal(b, &doc) // lenient: a mistyped key reads as undeclared
	for _, d := range []string{doc.Database.Migrations.Dir, doc.Database.MigrationsDir} {
		d = filepath.ToSlash(filepath.Clean(strings.TrimSpace(d)))
		if d != "" && d != "." && !filepath.IsAbs(d) && !strings.HasPrefix(d, "../") {
			return d
		}
	}
	return ""
}

// protoServiceDecls is every service declaration under dir, each with the
// comment block above it (where a `// forge:*` service marker lives), in path
// order. Editing a message or an rpc leaves it as it was, so it does not
// re-render the guide on every proto save.
//
// Each file is read whole on every call: a stamp (size, mtime) can survive a
// rewrite, and a memo keyed on it would keep serving the old declarations. A
// walk or read failure is written into the result as a marker, so a key built
// while part of the tree was unreadable differs from the one built once it is
// readable again, rather than caching a guide over an incomplete input set.
func protoServiceDecls(dir string) []byte {
	var out bytes.Buffer
	unreadable := func(path string) {
		out.WriteString(path)
		out.WriteString("\x00!unreadable\x00")
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path != dir || !errors.Is(err, fs.ErrNotExist) {
				unreadable(path)
			}
			return nil //nolint:nilerr // recorded in the key; the walk goes on
		}
		if d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		decls, ok := readProtoDecls(path)
		if !ok {
			unreadable(path)
			return nil
		}
		out.WriteString(path)
		out.WriteByte(0)
		out.Write(decls)
		return nil
	})
	if err != nil {
		unreadable(dir)
	}
	return out.Bytes()
}

// readProtoDecls is each `service` line of a proto file with the run of
// comment lines directly above it; false when the file cannot be read whole.
func readProtoDecls(path string) ([]byte, bool) {
	b, err := os.ReadFile(path) //nolint:gosec // a proto file under the project's proto/services
	if err != nil {
		return nil, false
	}
	var out, block bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "//"):
			block.WriteString(line)
			block.WriteByte('\n')
		case strings.HasPrefix(line, "service "):
			out.Write(block.Bytes())
			out.WriteString(line)
			out.WriteByte(0)
			block.Reset()
		default:
			block.Reset()
		}
	}
	if sc.Err() != nil {
		return nil, false
	}
	return out.Bytes(), true
}
