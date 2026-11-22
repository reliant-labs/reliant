package cli

import (
	"bufio"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// command_surface.txt freezes forge's v1 command surface: every path
// `forge project capabilities --json` lists, with every flag it takes. Forge
// is semver at major 1, so a path or flag in it is a contract: scripts,
// skills, CI workflows and reliant's own exec sites type them, against forges
// of every age. TestCommandSurfaceIsStable fails when one stops resolving,
// stops running, or loses a flag (or changes a flag's type or shorthand).
//
// Adding is free: a new command or flag passes without touching the file.
// Record it with -update-command-surface, which only ever ADDS lines and
// flags, so it cannot paper over a removal. Moving a command to a new noun is
// a row in deprecated_aliases.go; the old path stays here, and stays working.
//
// Because adding is free, main's file can lag the tree, and a bare
// -update-command-surface then records every command other changes shipped
// unrecorded. -command-surface-only names the paths to record (comma
// separated, each with the commands beneath it), so a change records its own
// lines and nothing else.
var updateCommandSurface = flag.Bool("update-command-surface", false,
	"add the current command tree's paths and flags to testdata/command_surface.txt (never removes)")

var commandSurfaceOnly = flag.String("command-surface-only", "",
	"with -update-command-surface: record only these comma-separated command paths (\"forge db analyze\") and the commands beneath them")

const commandSurfaceFile = "command_surface.txt"

const commandSurfaceHeader = `# forge's v1 command surface: every path 'forge project capabilities --json'
# lists, with each command's flags (name[/shorthand]:type). Every path here must
# keep resolving and running, with at least these flags, until v2
# (TestCommandSurfaceIsStable). To record additions:
#   go test ./internal/cli -run TestCommandSurfaceIsStable -update-command-surface \
#     -command-surface-only "forge <your command>"
# (without -command-surface-only it records every unrecorded path). The update
# only adds; a removal is a breaking change and fails the test.
`

// surfaceEntry is one frozen command.
type surfaceEntry struct {
	path  string
	group bool
	flags []string
}

func (e surfaceEntry) line() string {
	kind := "cmd"
	if e.group {
		kind = "group"
	}
	return e.path + "\t" + kind + "\t" + strings.Join(e.flags, " ")
}

// surfaceFlags lists every flag cmd accepts — its own and those it inherits —
// as name[/shorthand]:type, sorted. cobra's help and version flags are left
// out: cobra adds them lazily, and they are cobra's contract, not forge's.
func surfaceFlags(cmd *cobra.Command) []string {
	seen := map[string]bool{}
	var out []string
	add := func(f *pflag.Flag) {
		if f.Name == "help" || f.Name == "version" || seen[f.Name] {
			return
		}
		seen[f.Name] = true
		s := f.Name
		if f.Shorthand != "" {
			s += "/" + f.Shorthand
		}
		out = append(out, s+":"+f.Value.Type())
	}
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	sort.Strings(out)
	return out
}

// currentSurface is the live tree's surface, from the same walk
// `capabilities` makes.
func currentSurface(root *cobra.Command) map[string]surfaceEntry {
	out := map[string]surfaceEntry{}
	for _, spec := range commandSpecs(root) {
		cmd := resolveSurfacePath(root, spec.Path)
		if cmd == nil {
			continue
		}
		out[spec.Path] = surfaceEntry{path: spec.Path, group: spec.Group, flags: surfaceFlags(cmd)}
	}
	return out
}

// resolveSurfacePath finds the command at path ("forge a b") through cobra's
// own dispatch, or nil when the words do not name exactly one command.
func resolveSurfacePath(root *cobra.Command, path string) *cobra.Command {
	words := strings.Fields(path)
	if len(words) == 0 || words[0] != root.Name() {
		return nil
	}
	cmd, rest, err := root.Find(words[1:])
	if err != nil || cmd == nil || len(rest) != 0 || cmd.CommandPath() != path {
		return nil
	}
	return cmd
}

func readCommandSurface(t *testing.T) map[string]surfaceEntry {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", commandSurfaceFile))
	if err != nil {
		t.Fatalf("%v: record it with -update-command-surface", err)
	}
	defer f.Close()
	out := map[string]surfaceEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || (parts[1] != "cmd" && parts[1] != "group") {
			t.Fatalf("testdata/%s:%d: want <path>\\t<cmd|group>\\t<flags>, got %q", commandSurfaceFile, n, line)
		}
		out[parts[0]] = surfaceEntry{path: parts[0], group: parts[1] == "group", flags: strings.Fields(parts[2])}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeCommandSurface(t *testing.T, entries map[string]surfaceEntry) {
	t.Helper()
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString(commandSurfaceHeader)
	for _, p := range paths {
		b.WriteString(entries[p].line())
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join("testdata", commandSurfaceFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// onlySurface keeps the entries of current at or beneath one of the
// comma-separated paths in only; all of current when only is empty.
func onlySurface(current map[string]surfaceEntry, only string) map[string]surfaceEntry {
	var roots []string
	for _, p := range strings.Split(only, ",") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			roots = append(roots, p)
		}
	}
	if len(roots) == 0 {
		return current
	}
	out := map[string]surfaceEntry{}
	for p, e := range current {
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+" ") {
				out[p] = e
				break
			}
		}
	}
	return out
}

// mergeSurface is frozen plus everything current adds: new paths, and new
// flags on frozen paths. Nothing frozen is dropped.
func mergeSurface(frozen, current map[string]surfaceEntry) map[string]surfaceEntry {
	out := map[string]surfaceEntry{}
	for p, e := range frozen {
		out[p] = e
	}
	for p, cur := range current {
		old, ok := out[p]
		if !ok {
			out[p] = cur
			continue
		}
		have := map[string]bool{}
		for _, f := range old.flags {
			have[f] = true
		}
		flags := append([]string(nil), old.flags...)
		for _, f := range cur.flags {
			if !have[f] {
				flags = append(flags, f)
			}
		}
		sort.Strings(flags)
		old.flags = flags
		out[p] = old
	}
	return out
}

// TestCommandSurfaceIsStable: every frozen path still resolves to exactly
// one command, a frozen command is still runnable, and every frozen flag is
// still accepted with the same type and shorthand.
func TestCommandSurfaceIsStable(t *testing.T) {
	root := newTestRootCmd(t)
	if *updateCommandSurface {
		frozen := map[string]surfaceEntry{}
		if _, err := os.Stat(filepath.Join("testdata", commandSurfaceFile)); err == nil {
			frozen = readCommandSurface(t)
		}
		writeCommandSurface(t, mergeSurface(frozen, onlySurface(currentSurface(root), *commandSurfaceOnly)))
	}
	frozen := readCommandSurface(t)
	if len(frozen) < 200 {
		t.Fatalf("testdata/%s freezes only %d paths: it should hold forge's whole surface", commandSurfaceFile, len(frozen))
	}
	paths := make([]string, 0, len(frozen))
	for p := range frozen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		want := frozen[p]
		cmd := resolveSurfacePath(root, p)
		if cmd == nil {
			t.Errorf("`%s` no longer resolves to a command. It is v1 surface: keep it working (move it with a row in deprecated_aliases.go, never by deleting it)", p)
			continue
		}
		if !want.group && !cmd.Runnable() {
			t.Errorf("`%s` was a runnable command and is now only a group", p)
		}
		have := map[string]bool{}
		for _, f := range surfaceFlags(cmd) {
			have[f] = true
		}
		for _, f := range want.flags {
			if !have[f] {
				t.Errorf("`%s` no longer takes --%s (frozen as %s; now %v)", p, strings.SplitN(strings.SplitN(f, ":", 2)[0], "/", 2)[0], f, surfaceFlags(cmd))
			}
		}
	}
}

// TestResolveSurfacePathIsExact pins the resolver the surface test relies on:
// a path names exactly one command, never a prefix of one plus arguments.
func TestResolveSurfacePathIsExact(t *testing.T) {
	root := newTestRootCmd(t)
	for _, tc := range []struct {
		path string
		ok   bool
	}{
		{"forge env deploy", true},
		{"forge env", true},
		{"forge env no-such-verb", false},
		{"forge", true},
		{"reliant env deploy", false},
	} {
		if got := resolveSurfacePath(root, tc.path) != nil; got != tc.ok {
			t.Errorf("resolveSurfacePath(%q) = %v, want %v", tc.path, got, tc.ok)
		}
	}
}

// TestOnlySurfaceRecordsTheNamedPaths: -command-surface-only keeps a path and
// the commands beneath it, never a sibling that merely shares a prefix.
func TestOnlySurfaceRecordsTheNamedPaths(t *testing.T) {
	current := map[string]surfaceEntry{}
	for _, p := range []string{"forge db", "forge db analyze", "forge db analyze x", "forge dbx", "forge obs users"} {
		current[p] = surfaceEntry{path: p}
	}
	got := onlySurface(current, " forge  db analyze ,forge obs users")
	var paths []string
	for p := range got {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if want := "forge db analyze|forge db analyze x|forge obs users"; strings.Join(paths, "|") != want {
		t.Errorf("onlySurface = %v, want %s", paths, want)
	}
	if len(onlySurface(current, "")) != len(current) {
		t.Error("an empty -command-surface-only must record everything")
	}
}
