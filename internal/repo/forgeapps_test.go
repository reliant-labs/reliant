// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile writes content to root/rel, creating its directories.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// forgeManifestYAML is a forge project manifest naming name, as `forge
// project new` writes one.
func forgeManifestYAML(name string) string {
	return fmt.Sprintf("# Forge project manifest\nname: %s\nmodule_path: example.com/%s\nforge_version: v1.6.0\n", name, name)
}

func TestFindForgeApps_RootAndNestedApps(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("shop"))
	// A static site: forge_version only, no Go module.
	writeFile(t, repoRoot, "sites/landing/forge.yaml", "name: landing\nforge_version: v1.0.15\n")
	// An older manifest with neither key, beside its deploy/kcl tree.
	writeFile(t, repoRoot, "services/api/forge.yaml", "name: shop-api\n")
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "services/api/deploy/kcl"), 0o755))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.False(t, scan.Truncated, scan.TruncatedReason)
	assert.Equal(t, []ForgeApp{
		{Subpath: "", Name: "shop"},
		{Subpath: "services/api", Name: "shop-api"},
		{Subpath: "sites/landing", Name: "landing"},
	}, scan.Apps)
}

// A file called forge.yaml is not always a forge app: this workspace's own
// reliant repo has a workflow preset by that name, and dependency trees,
// generated code and fixtures carry them too.
func TestFindForgeApps_IgnoresWhatIsNotAnApp(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("reliant"))
	writeFile(t, repoRoot, "internal/workflow/builtin/presets/forge.yaml",
		"name: forge\ndescription: Balanced general-purpose agent\ntag: agent\n")
	writeFile(t, repoRoot, "node_modules/pkg/forge.yaml", forgeManifestYAML("dep"))
	writeFile(t, repoRoot, "vendor/x/forge.yaml", forgeManifestYAML("vendored"))
	writeFile(t, repoRoot, "gen/forge.yaml", forgeManifestYAML("generated"))
	writeFile(t, repoRoot, "internal/cli/testdata/forge.yaml", forgeManifestYAML("fixture"))
	writeFile(t, repoRoot, ".reliant/forge.yaml", forgeManifestYAML("hidden"))
	writeFile(t, repoRoot, "nameless/forge.yaml", "forge_version: v1.0.0\n")
	writeFile(t, repoRoot, "broken/forge.yaml", "name: [unterminated\n")
	// A nested checkout is its own repository; its apps are reported on it.
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "third_party/lib/.git"), 0o755))
	writeFile(t, repoRoot, "third_party/lib/forge.yaml", forgeManifestYAML("lib"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.False(t, scan.Truncated)
	assert.Equal(t, []ForgeApp{{Subpath: "", Name: "reliant"}}, scan.Apps)
}

// The root forge.yaml counts whenever it names something, exactly as
// projects.is_forge has always read it.
func TestFindForgeApps_RootNeedsOnlyAName(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", "name: '  barksocial  '\n")
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Equal(t, []ForgeApp{{Subpath: "", Name: "barksocial"}}, scan.Apps)
}

func TestFindForgeApps_DepthBound(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "a/b/c/d/forge.yaml", forgeManifestYAML("four-deep"))
	writeFile(t, repoRoot, "a/b/c/d/e/forge.yaml", forgeManifestYAML("five-deep"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Equal(t, []ForgeApp{{Subpath: "a/b/c/d", Name: "four-deep"}}, scan.Apps,
		"an app at depth %d is found and one deeper is not", ForgeAppMaxDepth)
	assert.False(t, scan.Truncated, "the depth bound is the walk's shape, not an early stop")

	scan, err = FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{MaxDepth: 2})
	require.NoError(t, err)
	assert.Empty(t, scan.Apps)
}

func TestFindForgeApps_EntryBudgetStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	for i := range 20 {
		require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, fmt.Sprintf("dir%02d", i)), 0o755))
	}
	writeFile(t, repoRoot, "zz/forge.yaml", forgeManifestYAML("late"))

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{MaxEntries: 5})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Contains(t, scan.TruncatedReason, "5 entries")
	assert.Empty(t, scan.Apps, "the walk stopped long before zz/")
}

func TestFindForgeApps_AppCapStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	for i := range MaxForgeAppsPerRepo + 3 {
		writeFile(t, repoRoot, fmt.Sprintf("apps/a%03d/forge.yaml", i), forgeManifestYAML(fmt.Sprintf("a%03d", i)))
	}
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.Len(t, scan.Apps, MaxForgeAppsPerRepo)
	assert.True(t, scan.Truncated)
	assert.Contains(t, scan.TruncatedReason, "forge apps")
}

// fixedPast is a deadline every run is already past, read from no clock.
var fixedPast = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func TestFindForgeApps_DeadlineStopsAndSaysSo(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "forge.yaml", forgeManifestYAML("shop"))
	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{Deadline: fixedPast})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Equal(t, "it ran out of time", scan.TruncatedReason)
}

// A symlinked directory or forge.yaml is not followed: it may point anywhere,
// another repository or outside the project. Driven through the FS seam, so
// it runs on every platform: real symlinks need privileges on Windows. The OS
// tree is os.DirFS, whose entries carry their own (Lstat) type exactly as
// this fixture's do.
func TestFindForgeApps_DoesNotFollowSymlinks(t *testing.T) {
	tree := fstest.MapFS{
		"forge.yaml":         {Data: []byte("../elsewhere/forge.yaml"), Mode: fs.ModeSymlink},
		"linked":             {Data: []byte("../elsewhere"), Mode: fs.ModeSymlink},
		"real/forge.yaml":    {Data: []byte(forgeManifestYAML("real"))},
		"real/sub/README.md": {Data: []byte("hi")},
	}
	scan, err := FindForgeApps(context.Background(), "repo", ForgeAppOptions{FS: tree})
	require.NoError(t, err)
	assert.False(t, scan.Truncated)
	assert.Equal(t, []ForgeApp{{Subpath: "real", Name: "real"}}, scan.Apps)
}

// deniedFS is fsys with the named paths unreadable: opening one fails with a
// permission error, as an unreadable directory or file does on disk. Only
// Open is exposed, so a directory listing goes through it too.
type deniedFS struct {
	fsys   fs.FS
	denied map[string]bool
}

func (d deniedFS) Open(name string) (fs.File, error) {
	if d.denied[name] {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return d.fsys.Open(name)
}

// What cannot be read is unknown, not absent. A scan that could not read a
// subtree or a manifest is reported Truncated, so the server keeps the apps
// it recorded there instead of deleting them (a partial scan never replaces
// the set: store.SetRepoForgeApps), while what it could read is still found.
func TestFindForgeApps_UnreadableIsUnknownNotAbsent(t *testing.T) {
	tree := fstest.MapFS{
		"forge.yaml":                {Data: []byte(forgeManifestYAML("shop"))},
		"services/api/forge.yaml":   {Data: []byte(forgeManifestYAML("shop-api"))},
		"sites/landing/forge.yaml":  {Data: []byte(forgeManifestYAML("landing"))},
		"workers/queue/forge.yaml":  {Data: []byte(forgeManifestYAML("queue"))},
		"workers/queue/deploy/x.k":  {Data: []byte("x")},
		"docs/forge.yaml":           {Data: []byte("name: [unterminated\n")},
		"tools/gen-config/main.go":  {Data: []byte("package main")},
		"tools/gen-config/.git":     {Data: []byte("gitdir: elsewhere")},
		"tools/gen-config/app.yaml": {Data: []byte("x")},
	}

	for _, tc := range []struct {
		name, denied string
		want         []ForgeApp
	}{
		{"an unreadable subtree", "services", []ForgeApp{
			{Subpath: "", Name: "shop"}, {Subpath: "sites/landing", Name: "landing"}, {Subpath: "workers/queue", Name: "queue"}}},
		{"an unreadable manifest", "sites/landing/forge.yaml", []ForgeApp{
			{Subpath: "", Name: "shop"}, {Subpath: "services/api", Name: "shop-api"}, {Subpath: "workers/queue", Name: "queue"}}},
		{"an unreadable root manifest", "forge.yaml", []ForgeApp{
			{Subpath: "services/api", Name: "shop-api"}, {Subpath: "sites/landing", Name: "landing"}, {Subpath: "workers/queue", Name: "queue"}}},
		{"an unreadable .git: whose apps they are cannot be told", "tools/gen-config/.git", []ForgeApp{
			{Subpath: "", Name: "shop"}, {Subpath: "services/api", Name: "shop-api"},
			{Subpath: "sites/landing", Name: "landing"}, {Subpath: "workers/queue", Name: "queue"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := FindForgeApps(context.Background(), "repo",
				ForgeAppOptions{FS: deniedFS{fsys: tree, denied: map[string]bool{tc.denied: true}}})
			require.NoError(t, err)
			assert.True(t, scan.Truncated, "an unread part of the tree must not read as a complete scan")
			assert.Equal(t, "it could not read "+tc.denied, scan.TruncatedReason)
			assert.Equal(t, tc.want, scan.Apps)
		})
	}

	// The same tree read whole: complete. A manifest that is not YAML is not
	// an app, and is not a read failure either.
	scan, err := FindForgeApps(context.Background(), "repo", ForgeAppOptions{FS: tree})
	require.NoError(t, err)
	assert.False(t, scan.Truncated, scan.TruncatedReason)
	assert.Len(t, scan.Apps, 4)
}

// A nested forge.yaml with neither key is an app only beside a deploy/kcl
// tree; when that cannot be looked at, it is unknown.
func TestFindForgeApps_UnreadableDeployTreeIsUnknown(t *testing.T) {
	tree := fstest.MapFS{
		"services/api/forge.yaml":        {Data: []byte("name: shop-api\n")},
		"services/api/deploy/kcl/main.k": {Data: []byte("x")},
	}
	scan, err := FindForgeApps(context.Background(), "repo", ForgeAppOptions{FS: tree})
	require.NoError(t, err)
	assert.Equal(t, []ForgeApp{{Subpath: "services/api", Name: "shop-api"}}, scan.Apps)

	scan, err = FindForgeApps(context.Background(), "repo",
		ForgeAppOptions{FS: deniedFS{fsys: tree, denied: map[string]bool{"services/api/deploy/kcl": true}}})
	require.NoError(t, err)
	assert.Empty(t, scan.Apps)
	assert.True(t, scan.Truncated)
	// The walk meets the unreadable directory itself as well as the
	// manifest that needed it.
	assert.Equal(t, "it could not read services/api/deploy/kcl and 1 more", scan.TruncatedReason)
}

func TestFindForgeApps_MissingRootIsAnError(t *testing.T) {
	_, err := FindForgeApps(context.Background(), filepath.Join(t.TempDir(), "gone"), ForgeAppOptions{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "repository path"), err.Error())
}

// wideFS is tree with the directory at wide replaced by a synthetic one of
// size entries, generated as they are asked for: a directory far wider than
// any test could write to disk. served counts the entries handed out, and
// onBatch, when set, runs after each batch.
type wideFS struct {
	t       *testing.T
	tree    fstest.MapFS
	wide    string
	size    int
	served  int
	batches int
	onBatch func()
}

func (w *wideFS) Open(name string) (fs.File, error) {
	if name == w.wide {
		return &wideDir{fsys: w}, nil
	}
	return w.tree.Open(name)
}

type wideDir struct{ fsys *wideFS }

func (d *wideDir) Stat() (fs.FileInfo, error) {
	return wideInfo{name: path.Base(d.fsys.wide), dir: true}, nil
}
func (d *wideDir) Read([]byte) (int, error) { return 0, io.EOF }
func (d *wideDir) Close() error             { return nil }

func (d *wideDir) ReadDir(n int) ([]fs.DirEntry, error) {
	w := d.fsys
	// A listing of the whole directory at once is the stall this bounds.
	if n <= 0 || n > forgeAppReadBatch {
		w.t.Errorf("ReadDir(%d): a wide directory must be read in batches of at most %d", n, forgeAppReadBatch)
	}
	if w.served >= w.size {
		return nil, io.EOF
	}
	n = min(n, w.size-w.served)
	out := make([]fs.DirEntry, 0, n)
	for range n {
		out = append(out, fs.FileInfoToDirEntry(wideInfo{name: fmt.Sprintf("f%010d.txt", w.served)}))
		w.served++
	}
	w.batches++
	if w.onBatch != nil {
		w.onBatch()
	}
	return out, nil
}

type wideInfo struct {
	name string
	dir  bool
}

func (i wideInfo) Name() string { return i.name }
func (i wideInfo) Size() int64  { return 0 }
func (i wideInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i wideInfo) ModTime() time.Time { return time.Time{} }
func (i wideInfo) IsDir() bool        { return i.dir }
func (i wideInfo) Sys() any           { return nil }

// wideTree is a repository with an app, then a directory "wide" of size
// entries: the app sorts first, so a scan the wide directory stops has still
// found it.
func wideTree(t *testing.T, size int) *wideFS {
	return &wideFS{t: t, wide: "wide", size: size, tree: fstest.MapFS{
		"apps/shop/forge.yaml": {Data: []byte(forgeManifestYAML("shop"))},
		"wide/placeholder":     {Data: []byte("lists wide as a directory; its listing is wideDir's")},
	}}
}

// One directory of a billion entries: the entry budget stops the scan within
// one entry of the budget, never after listing (or sorting) the directory
// whole, and the scan reports what it found as partial.
func TestFindForgeApps_WideDirectoryStopsWithinBudget(t *testing.T) {
	const budget = 1_000
	fsys := wideTree(t, 1_000_000_000)
	scan, err := FindForgeApps(context.Background(), "repo", ForgeAppOptions{FS: fsys, MaxEntries: budget})
	require.NoError(t, err)
	assert.True(t, scan.Truncated, "a scan the budget stopped is partial")
	assert.Equal(t, fmt.Sprintf("it looked at %d entries", budget), scan.TruncatedReason)
	assert.Equal(t, []ForgeApp{{Subpath: "apps/shop", Name: "shop"}}, scan.Apps)
	assert.LessOrEqual(t, fsys.served, budget+1, "the wide directory is read no further than the budget")

	// The default budget bounds it the same way.
	fsys = wideTree(t, 1_000_000_000)
	scan, err = FindForgeApps(context.Background(), "repo", ForgeAppOptions{FS: fsys})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Equal(t, []ForgeApp{{Subpath: "apps/shop", Name: "shop"}}, scan.Apps)
	assert.LessOrEqual(t, fsys.served, DefaultForgeAppMaxEntries+1)
}

// The deadline and the request are checked between the batches of one
// directory's listing, not only between the entries a walk visits.
func TestFindForgeApps_WideDirectoryStopsBetweenBatches(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, fsys *wideFS) (context.Context, ForgeAppOptions)
	}{
		{"the request ends", "the request ended", func(t *testing.T, fsys *wideFS) (context.Context, ForgeAppOptions) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			fsys.onBatch = cancel
			return ctx, ForgeAppOptions{}
		}},
		{"the deadline passes", "it ran out of time", func(t *testing.T, fsys *wideFS) (context.Context, ForgeAppOptions) {
			deadline := time.Now().Add(5 * time.Millisecond)
			// The first batch outlasts the deadline.
			fsys.onBatch = func() {
				for time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			}
			return context.Background(), ForgeAppOptions{Deadline: deadline}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := wideTree(t, 1_000_000_000)
			ctx, opts := tc.setup(t, fsys)
			opts.FS = fsys
			opts.MaxEntries = 1 << 30
			scan, err := FindForgeApps(ctx, "repo", opts)
			require.NoError(t, err)
			assert.True(t, scan.Truncated)
			assert.Equal(t, tc.reason, scan.TruncatedReason)
			assert.Equal(t, []ForgeApp{{Subpath: "apps/shop", Name: "shop"}}, scan.Apps)
			assert.Equal(t, 1, fsys.batches, "the walk stops before the next batch")
		})
	}
}

// shuffledFS lists every directory of tree in an order seeded by seed, a few
// entries at a time, as a filesystem with its own listing order would.
type shuffledFS struct {
	tree fstest.MapFS
	seed int64
}

func (s shuffledFS) Open(name string) (fs.File, error) {
	f, err := s.tree.Open(name)
	if err != nil {
		return nil, err
	}
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		return f, nil
	}
	list, err := rd.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	rand.New(rand.NewSource(s.seed)).Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
	return &shuffledDir{ReadDirFile: rd, list: list}, nil
}

type shuffledDir struct {
	fs.ReadDirFile
	list []fs.DirEntry
}

func (d *shuffledDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		out := d.list
		d.list = nil
		return out, nil
	}
	if len(d.list) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(d.list), 7)
	out := d.list[:n]
	d.list = d.list[n:]
	return out, nil
}

// The same tree gives the same scan whatever order the filesystem lists a
// directory in, even when the budget stops it part-way: which apps a partial
// scan saw must not flap between two scans of an unchanged repository.
func TestFindForgeApps_PartialScanIsDeterministic(t *testing.T) {
	tree := fstest.MapFS{}
	for i := range 100 {
		tree[fmt.Sprintf("apps/a%03d/forge.yaml", i)] = &fstest.MapFile{Data: []byte(forgeManifestYAML(fmt.Sprintf("a%03d", i)))}
	}
	// apps/ and its listing are 101 entries; each app directory is one
	// more, so the budget stops the walk part-way through them.
	opts := ForgeAppOptions{MaxEntries: 130}

	opts.FS = tree
	want, err := FindForgeApps(context.Background(), "repo", opts)
	require.NoError(t, err)
	require.True(t, want.Truncated)
	require.NotEmpty(t, want.Apps)
	require.Less(t, len(want.Apps), 100)
	assert.Equal(t, "a000", want.Apps[0].Name, "a partial scan walks in name order")

	for seed := range int64(5) {
		opts.FS = shuffledFS{tree: tree, seed: seed}
		got, err := FindForgeApps(context.Background(), "repo", opts)
		require.NoError(t, err)
		assert.Equal(t, want, got, "seed %d", seed)
	}
}

// A wide directory on disk: os.DirFS's listing is read in batches too.
func TestFindForgeApps_WideDirectoryOnDisk(t *testing.T) {
	repoRoot := t.TempDir()
	writeFile(t, repoRoot, "apps/shop/forge.yaml", forgeManifestYAML("shop"))
	wide := filepath.Join(repoRoot, "wide")
	require.NoError(t, os.Mkdir(wide, 0o755))
	for i := range 2_000 {
		f, err := os.Create(filepath.Join(wide, fmt.Sprintf("f%05d", i)))
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	scan, err := FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{MaxEntries: 500})
	require.NoError(t, err)
	assert.True(t, scan.Truncated)
	assert.Equal(t, "it looked at 500 entries", scan.TruncatedReason)
	assert.Equal(t, []ForgeApp{{Subpath: "apps/shop", Name: "shop"}}, scan.Apps)

	scan, err = FindForgeApps(context.Background(), repoRoot, ForgeAppOptions{})
	require.NoError(t, err)
	assert.False(t, scan.Truncated, scan.TruncatedReason)
	assert.Equal(t, []ForgeApp{{Subpath: "apps/shop", Name: "shop"}}, scan.Apps)
}
