package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// previewProject is a project with a committed template env "preview".
func previewProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "deploy", "kcl", "preview")
	if err := os.MkdirAll(tmpl, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"main.k": "# template main\n", "config.k": "# template config\n"} {
		if err := os.WriteFile(filepath.Join(tmpl, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakePreviewWorld stubs the render (namespace "demo-<env>" on k3d-demo, or a
// fixed one), the clock and kubectl, and restores them after the test.
type fakePreviewWorld struct {
	fixedNamespace string
	hosted         bool
	namespaces     map[string]map[string]string // ns -> labels on the live cluster
	annotations    map[string]string            // ns -> expires-at
	deleted        []string
	hostedDeleted  []string
}

func installPreviewWorld(t *testing.T, w *fakePreviewWorld, now time.Time) {
	t.Helper()
	oldRender, oldNow, oldKubectl, oldHosted := previewRender, previewNow, previewKubectl, previewDeleteHosted
	t.Cleanup(func() {
		previewRender, previewNow, previewKubectl, previewDeleteHosted = oldRender, oldNow, oldKubectl, oldHosted
	})
	previewNow = func() time.Time { return now }
	previewRender = func(_ context.Context, _ string, env string) (*KCLEntities, error) {
		ns := "demo-" + env
		if w.fixedNamespace != "" {
			ns = w.fixedNamespace
		}
		e := &KCLEntities{ManifestNamespaces: []RenderedNamespaceEntity{{Cluster: "k3d-demo", Name: ns}}}
		if w.hosted {
			e.ControlPlane = &ControlPlaneEntity{}
		}
		return e, nil
	}
	previewDeleteHosted = func(_ context.Context, name string, _ io.Writer) error {
		w.hostedDeleted = append(w.hostedDeleted, name)
		return nil
	}
	previewKubectl = func(_ context.Context, kctx string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "get" && args[1] == "namespace":
			labels, ok := w.namespaces[args[2]]
			if !ok {
				return nil, nil
			}
			return json.Marshal(map[string]any{"metadata": map[string]any{"name": args[2], "labels": labels}})
		case args[0] == "get" && args[1] == "namespaces":
			var items []any
			for ns, labels := range w.namespaces {
				items = append(items, map[string]any{"metadata": map[string]any{
					"name": ns, "labels": labels, "annotations": map[string]string{previewExpiresAnnot: w.annotations[ns]},
				}})
			}
			return json.Marshal(map[string]any{"items": items})
		case args[0] == "delete":
			w.deleted = append(w.deleted, kctx+"/"+args[2])
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected kubectl %v", args)
	}
}

func TestPreviewCreate(t *testing.T) {
	dir := previewProject(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	installPreviewWorld(t, &fakePreviewWorld{}, now)
	f := previewKeyFlags{pr: 42, from: "preview"}

	doc, err := createPreview(context.Background(), dir, f, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Name != "preview-pr-42" || doc.Refreshed || doc.ExpiresAt != "2026-11-14T12:00:00Z" || doc.Source != "pr-42" ||
		len(doc.Namespaces) != 1 || doc.Namespaces[0] != "k3d-demo/demo-preview-pr-42" {
		t.Fatalf("doc: %+v", doc)
	}
	inst := filepath.Join(dir, "deploy", "kcl", "preview-pr-42")
	for _, f := range []string{"main.k", "config.k", previewHeaderFileName} {
		if _, err := os.Stat(filepath.Join(inst, f)); err != nil {
			t.Errorf("instance is missing %s: %v", f, err)
		}
	}
	m, ok, err := kclrender.ReadPreviewMarker(inst)
	if err != nil || !ok || m.Of != "preview" || m.ExpiresAt != doc.ExpiresAt {
		t.Fatalf("marker: %+v %v %v", m, ok, err)
	}

	// Re-running refreshes the expiry.
	installPreviewWorld(t, &fakePreviewWorld{}, now.Add(24*time.Hour))
	doc, err = createPreview(context.Background(), dir, f, 72*time.Hour)
	if err != nil || !doc.Refreshed || doc.ExpiresAt != "2026-11-15T12:00:00Z" {
		t.Fatalf("refresh: %+v %v", doc, err)
	}

	// A hand-written env of that name is never overwritten.
	hand := filepath.Join(dir, "deploy", "kcl", "preview-pr-7")
	if err := os.MkdirAll(hand, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := createPreview(context.Background(), dir, previewKeyFlags{pr: 7, from: "preview"}, time.Hour); err == nil || !strings.Contains(err.Error(), "not a preview") {
		t.Errorf("hand-written env: %v", err)
	}
	// A template that is itself an instance is refused.
	if _, err := createPreview(context.Background(), dir, previewKeyFlags{pr: 1, from: "preview-pr-42"}, time.Hour); err == nil || !strings.Contains(err.Error(), "itself a preview") {
		t.Errorf("instance as template: %v", err)
	}
}

// An instance that would render into the template env's own namespace is
// refused, and the copy it made is removed.
func TestPreviewCreateRefusesTheTemplatesNamespace(t *testing.T) {
	dir := previewProject(t)
	installPreviewWorld(t, &fakePreviewWorld{fixedNamespace: "demo-staging"}, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	_, err := createPreview(context.Background(), dir, previewKeyFlags{pr: 3, from: "preview"}, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "k3d-demo/demo-staging") || !strings.Contains(err.Error(), `option(\"env\")`) && !strings.Contains(err.Error(), `option("env")`) {
		t.Fatalf("want a namespace clash refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "kcl", "preview-pr-3")); !os.IsNotExist(err) {
		t.Errorf("the refused instance was left behind: %v", err)
	}
}

// A refresh refused after the copy went in (a namespace clash, or a render
// that fails) puts the previous instance back exactly, expiry included.
func TestPreviewCreateRefusedRefreshKeepsThePreviousInstance(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		world func(t *testing.T)
		want  string
	}{
		{"namespace clash", func(t *testing.T) {
			installPreviewWorld(t, &fakePreviewWorld{fixedNamespace: "demo-staging"}, now.Add(24*time.Hour))
		}, "demo-staging"},
		{"render fails", func(t *testing.T) {
			installPreviewWorld(t, &fakePreviewWorld{}, now.Add(24*time.Hour))
			previewRender = func(context.Context, string, string) (*KCLEntities, error) {
				return nil, fmt.Errorf("kcl: boom")
			}
		}, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := previewProject(t)
			installPreviewWorld(t, &fakePreviewWorld{}, now)
			f := previewKeyFlags{pr: 9, from: "preview"}
			first, err := createPreview(context.Background(), dir, f, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			inst := filepath.Join(dir, "deploy", "kcl", "preview-pr-9")
			if err := os.WriteFile(filepath.Join(dir, "deploy", "kcl", "preview", "main.k"), []byte("# template v2\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			tc.world(t)
			if _, err := createPreview(context.Background(), dir, f, time.Hour); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
			m, ok, err := kclrender.ReadPreviewMarker(inst)
			if err != nil || !ok || m.ExpiresAt != first.ExpiresAt {
				t.Errorf("marker after a refused refresh: %+v %v %v, want the previous expiry %s", m, ok, err, first.ExpiresAt)
			}
			if b, _ := os.ReadFile(filepath.Join(inst, "main.k")); string(b) != "# template main\n" {
				t.Errorf("main.k = %q, want the previous instance's content", b)
			}
			entries, _ := os.ReadDir(filepath.Join(dir, "deploy", "kcl"))
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") {
					t.Errorf("a staging directory was left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestPreviewDelete(t *testing.T) {
	dir := previewProject(t)
	w := &fakePreviewWorld{hosted: true, namespaces: map[string]map[string]string{
		"demo-preview-pr-42": {previewOfLabel: "preview"},
	}}
	installPreviewWorld(t, w, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	f := previewKeyFlags{pr: 42, from: "preview"}
	// A fresh checkout: the instance directory does not exist yet.
	if err := deletePreview(context.Background(), dir, f, "preview-pr-42", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(w.hostedDeleted) != 1 || w.hostedDeleted[0] != "preview-pr-42" {
		t.Errorf("hosted deletes: %v", w.hostedDeleted)
	}
	if len(w.deleted) != 1 || w.deleted[0] != "k3d-demo/demo-preview-pr-42" {
		t.Errorf("namespace deletes: %v", w.deleted)
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "kcl", "preview-pr-42")); !os.IsNotExist(err) {
		t.Errorf("instance directory left behind: %v", err)
	}

	// A namespace the preview label does not mark is left alone.
	w2 := &fakePreviewWorld{namespaces: map[string]map[string]string{"demo-preview-pr-43": {}}}
	installPreviewWorld(t, w2, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	if err := deletePreview(context.Background(), dir, previewKeyFlags{pr: 43, from: "preview"}, "preview-pr-43", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(w2.deleted) != 0 {
		t.Errorf("an unlabelled namespace was deleted: %v", w2.deleted)
	}

	// An ordinary env is never deleted as a preview.
	if err := deletePreview(context.Background(), dir, f, "preview", io.Discard); err == nil || !strings.Contains(err.Error(), "not a preview instance") {
		t.Errorf("ordinary env: %v", err)
	}
}

func TestFindExpiredPreviews(t *testing.T) {
	dir := previewProject(t)
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	w := &fakePreviewWorld{
		namespaces: map[string]map[string]string{
			"demo-preview-pr-1":  {previewOfLabel: "preview", previewInstanceLabel: "preview-pr-1"},
			"demo-preview-pr-2":  {previewOfLabel: "preview", previewInstanceLabel: "preview-pr-2"},
			"demo-preview-bad":   {previewOfLabel: "preview", previewInstanceLabel: "preview-bad"},
			"demo-other-pr-9":    {previewOfLabel: "other", previewInstanceLabel: "other-pr-9"},
			"demo-preview-pr-10": {previewOfLabel: "preview", previewInstanceLabel: "preview-pr-10"},
		},
		annotations: map[string]string{
			"demo-preview-pr-1":  "2026-11-02T00:00:00Z", // expired
			"demo-preview-pr-2":  "2026-12-01T00:00:00Z", // live
			"demo-preview-bad":   "whenever",             // unreadable: never expired
			"demo-other-pr-9":    "2026-11-02T00:00:00Z", // another template's
			"demo-preview-pr-10": "2026-11-02T00:00:00Z", // a local instance's: collected as the instance
		},
	}
	installPreviewWorld(t, w, created)
	if _, err := createPreview(context.Background(), dir, previewKeyFlags{pr: 10, from: "preview"}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	installPreviewWorld(t, w, created.Add(10*24*time.Hour))
	got, err := findExpiredPreviews(context.Background(), dir, []string{"preview"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Kind+":"+c.Name)
	}
	if want := "instance:preview-pr-10,namespace:demo-preview-pr-1"; strings.Join(names, ",") != want {
		t.Fatalf("expired: %v, want %s", names, want)
	}
}

// A refresh makes the instance exactly the template again: a nested file the
// template renamed or removed does not linger in the copy, and nothing the
// template does not hold (but the marker and header) is left behind.
func TestPreviewCreateRefreshMatchesTheTemplateExactly(t *testing.T) {
	dir := previewProject(t)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	installPreviewWorld(t, &fakePreviewWorld{}, now)
	tmpl := filepath.Join(dir, "deploy", "kcl", "preview")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(tmpl, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("overlays/api.k", "# api\n")
	write("overlays/old.k", "# old\n")
	write("values/prod.yaml", "a: 1\n")
	f := previewKeyFlags{pr: 5, from: "preview"}
	if _, err := createPreview(context.Background(), dir, f, time.Hour); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "deploy", "kcl", "preview-pr-5")

	// The template moves on: a nested rename, a nested removal, a new file.
	if err := os.Rename(filepath.Join(tmpl, "overlays", "old.k"), filepath.Join(tmpl, "overlays", "renamed.k")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(tmpl, "values")); err != nil {
		t.Fatal(err)
	}
	write("overlays/api.k", "# api v2\n")
	// And something stray in the instance itself.
	if err := os.WriteFile(filepath.Join(inst, "stray.k"), []byte("# stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := createPreview(context.Background(), dir, f, time.Hour)
	if err != nil || !doc.Refreshed {
		t.Fatalf("refresh: %+v %v", doc, err)
	}

	var got []string
	err = filepath.WalkDir(inst, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(inst, p)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{kclrender.PreviewMarkerFile, previewHeaderFileName, "config.k", "main.k", "overlays/api.k", "overlays/renamed.k"}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("instance holds %v, want exactly the template plus marker and header: %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(inst, "overlays", "api.k")); string(b) != "# api v2\n" {
		t.Errorf("overlays/api.k = %q, want the template's current content", b)
	}
	if m, ok, err := kclrender.ReadPreviewMarker(inst); err != nil || !ok || m.Of != "preview" {
		t.Errorf("marker after refresh: %+v %v %v", m, ok, err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "deploy", "kcl"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a staging directory was left behind: %s", e.Name())
		}
	}
}

// A symlink in the template is refused by name, and the existing instance is
// left as it was.
func TestPreviewCreateRefusesASymlinkInTheTemplate(t *testing.T) {
	dir := previewProject(t)
	installPreviewWorld(t, &fakePreviewWorld{}, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	tmpl := filepath.Join(dir, "deploy", "kcl", "preview")
	if err := os.Symlink("main.k", filepath.Join(tmpl, "link.k")); err != nil {
		t.Skipf("host resource missing: cannot create a symlink: %v", err)
	}
	_, err := createPreview(context.Background(), dir, previewKeyFlags{pr: 6, from: "preview"}, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "link.k") {
		t.Fatalf("err = %v, want a refusal naming the symlink", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "kcl", "preview-pr-6")); !os.IsNotExist(err) {
		t.Errorf("a refused create left an instance: %v", err)
	}
}

// GC judges a cluster namespace on its own expiry unless the local instance
// it belongs to is of the SAME template: a same-named directory of another
// template, or a local directory that was removed, does not hide an expired
// namespace; a live local instance's namespace is kept.
func TestFindExpiredPreviewsDedupesOnlyAgainstTheSameTemplate(t *testing.T) {
	dir := previewProject(t)
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	expired := "2026-11-02T00:00:00Z"
	w := &fakePreviewWorld{
		namespaces: map[string]map[string]string{
			"demo-live":    {previewOfLabel: "preview", previewInstanceLabel: "preview-pr-20"}, // a live local instance's
			"demo-other":   {previewOfLabel: "preview", previewInstanceLabel: "other-pr-21"},   // local dir is another template's
			"demo-removed": {previewOfLabel: "preview", previewInstanceLabel: "preview-pr-22"}, // local dir removed
		},
		annotations: map[string]string{"demo-live": expired, "demo-other": expired, "demo-removed": expired},
	}
	installPreviewWorld(t, w, created)
	// other: a second template, with an instance named like the label above.
	other := filepath.Join(dir, "deploy", "kcl", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "main.k"), []byte("# other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []previewKeyFlags{{pr: 20, from: "preview"}, {pr: 21, from: "other"}, {pr: 22, from: "preview"}} {
		if _, err := createPreview(context.Background(), dir, f, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, "deploy", "kcl", "preview-pr-22")); err != nil {
		t.Fatal(err)
	}
	installPreviewWorld(t, w, created.Add(5*24*time.Hour))
	got, err := findExpiredPreviews(context.Background(), dir, []string{"preview"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Kind+":"+c.Name)
	}
	sort.Strings(names)
	if want := "namespace:demo-other,namespace:demo-removed"; strings.Join(names, ",") != want {
		t.Fatalf("expired: %v, want %s (the live instance's namespace kept)", names, want)
	}
}
