package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/release"
)

func snapshotFrontendFixture(t *testing.T, names ...string) (release.Snapshot, *KCLEntities) {
	t.Helper()
	s := release.Snapshot{Environment: "prod", Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	entities := &KCLEntities{}
	for _, name := range names {
		src := release.Source{Repo: "github.com/example/frontend", Ref: "v1", Subdir: "web", Commit: strings.Repeat("a", 40)}
		artifact := release.SnapshotArtifact{Kind: release.KindGit, Location: src.Repo, Content: src.Commit, Origin: release.ArtifactOrigin{Source: &src, Release: "release-1"}}
		id, err := release.ArtifactIdentity(artifact)
		if err != nil {
			t.Fatal(err)
		}
		s.Artifacts[id] = artifact
		k := release.ConsumerKey{Cluster: "frontend", Workload: name}
		s.Selected = append(s.Selected, k)
		s.Consumers = append(s.Consumers, release.Consumer{Key: k, Slots: []string{"frontend"}, DependenciesDeclared: true})
		s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: k, Slot: "frontend", Artifact: id})
		entities.Frontends = append(entities.Frontends, FrontendEntity{Name: name, Type: "vite", Source: &config.GitSource{Repo: src.Repo, Ref: src.Ref, Subdir: src.Subdir}, SourceCommit: src.Commit, Path: "/must/not/freeze/cache", PublicDir: "dist", EnvVars: []KCLEnvVar{{Name: "VITE_API_URL", Value: "https://frozen.example"}}, Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase, Firebase: &FirebaseRuntime{Project: "frozen-project", Site: name}}})
	}
	return s, entities
}
func captureFrontendFixture(t *testing.T, s *release.Snapshot, base *release.Snapshot, e *KCLEntities) {
	t.Helper()
	if err := captureSnapshotFrontendsWith(context.Background(), s, base, e, "unused", "prod", func(string, string, map[string]map[string]string) (map[string]string, error) {
		m := map[string]string{}
		for _, f := range e.Frontends {
			m[f.Name] = "FROZEN_CONFIG_" + f.Name
		}
		return m, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFrontendFrozenPublishSelectedOnly(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "selected", "carried")
	captureFrontendFixture(t, &s, nil, e)
	s.Selected = s.Selected[:1]
	// Current runtime fields and source declarations cannot reach deploy.
	e.Frontends[0].Runtime.Firebase.Project = "CURRENT_WRONG_PROJECT"
	e.Frontends[0].Source.Ref = "moving-main"
	var resolved, published, preflights int
	adapters := snapshotFrontendAdapters{
		resolve: func(_ context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			resolved++
			if src.Commit != strings.Repeat("a", 40) || src.Ref != "v1" {
				t.Fatal("not frozen source")
			}
			return gitsource.Resolution{Dir: "/pinned", Commit: src.Commit}, nil, nil
		},
		preflight: func(_ context.Context, f FrontendEntity) error {
			preflights++
			if f.Path != filepath.FromSlash("/pinned/web") {
				t.Fatal("current checkout leaked")
			}
			return nil
		},
		publish: func(_ context.Context, f FrontendEntity, js, env string) error {
			published++
			if f.Name != "selected" || f.Runtime.Firebase.Project != "frozen-project" || f.SourceCommit != strings.Repeat("a", 40) || f.Release != "release-1" || js != "FROZEN_CONFIG_selected" || env != "prod" {
				t.Fatalf("non-frozen publication: %+v %s", f, js)
			}
			return nil
		},
	}
	if err := snapshotPublishFrontendsWith(context.Background(), s, true, adapters); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || preflights != 1 || published != 0 {
		t.Fatal("preflight published or visited carry")
	}
	if err := snapshotPublishFrontendsWith(context.Background(), s, false, adapters); err != nil {
		t.Fatal(err)
	}
	if published != 1 || resolved != 2 {
		t.Fatal("selected publication missing or carry published")
	}
}
func TestSnapshotFrontendCarryUsesBaseAndDoesNotRender(t *testing.T) {
	base, e := snapshotFrontendFixture(t, "web")
	captureFrontendFixture(t, &base, nil, e)
	candidate := base
	candidate.Assets = nil
	candidate.Selected = nil
	e.Frontends[0].Runtime.Firebase.Project = "current"
	if err := captureSnapshotFrontendsWith(context.Background(), &candidate, &base, e, "unused", "prod", func(string, string, map[string]map[string]string) (map[string]string, error) {
		t.Fatal("carried config re-rendered")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(candidate.Assets[0].Data) != string(base.Assets[0].Data) {
		t.Fatal("carried declaration changed")
	}
	if err := snapshotPublishFrontendsWith(context.Background(), candidate, false, snapshotFrontendAdapters{}); err != nil {
		t.Fatal(err)
	}
}
func TestSnapshotFrontendAllPreflightsBeforePublication(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "a", "b")
	captureFrontendFixture(t, &s, nil, e)
	calls := 0
	err := snapshotPublishFrontendsWith(context.Background(), s, false, snapshotFrontendAdapters{
		resolve: func(_ context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			return gitsource.Resolution{Dir: "fake", Commit: src.Commit}, nil, nil
		},
		preflight: func(_ context.Context, f FrontendEntity) error {
			if f.Name == "b" {
				return errors.New("missing firebase")
			}
			return nil
		},
		publish: func(context.Context, FrontendEntity, string, string) error { calls++; return nil },
	})
	if err == nil || calls != 0 {
		t.Fatal("publication began before all preflights")
	}
}
func TestSnapshotFrontendRejectsUnpinnedOrCorrupt(t *testing.T) {
	for _, mode := range []string{"override", "wrong-commit", "bundle", "private-env", "missing-asset", "digest", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s, e := snapshotFrontendFixture(t, "web")
			switch mode {
			case "override":
				e.Frontends[0].SourceOverridden = true
			case "wrong-commit":
				e.Frontends[0].SourceCommit = strings.Repeat("b", 40)
			case "bundle":
				e.Frontends[0].Bundle = []BundleDir{{Src: "current/docs"}}
			case "private-env":
				e.Frontends[0].EnvVars = []KCLEnvVar{{Name: "TOKEN", SecretRef: "secret"}}
			}
			err := captureSnapshotFrontendsWith(context.Background(), &s, nil, e, "unused", "prod", func(string, string, map[string]map[string]string) (map[string]string, error) {
				return map[string]string{}, nil
			})
			if mode == "override" || mode == "wrong-commit" || mode == "bundle" || mode == "private-env" {
				if err == nil {
					t.Fatal("unsafe capture accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-asset":
				s.Assets = nil
			case "digest":
				s.Assets[0].Data = append(s.Assets[0].Data, ' ')
			case "duplicate":
				s.Assets = append(s.Assets, s.Assets[0])
			}
			if _, err := snapshotFrontendEntries(s); err == nil {
				t.Fatal("bad frozen asset accepted")
			}
		})
	}
}

type snapshotFrontendFakeFetcher struct {
	commit string
	got    gitsource.Source
}

func (f *snapshotFrontendFakeFetcher) Fetch(_ context.Context, src gitsource.Source, dst string) (string, error) {
	f.got = src
	return f.commit, os.MkdirAll(dst, 0755)
}
func TestSnapshotFrontendActualResolverUsesCommitNotRef(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "web")
	captureFrontendFixture(t, &s, nil, e)
	fetcher := &snapshotFrontendFakeFetcher{commit: strings.Repeat("a", 40)}
	root := t.TempDir()
	resolver, err := gitsource.NewResolver(root, gitsource.WithCacheRoot(filepath.Join(root, "cache")), gitsource.WithOverrides(map[string]string{}), gitsource.WithFetcher(fetcher))
	if err != nil {
		t.Fatal(err)
	}
	err = snapshotPublishFrontendsWith(context.Background(), s, true, snapshotFrontendAdapters{
		resolve: func(ctx context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			r, err := resolver.Resolve(ctx, gitsource.Source{Repo: src.Repo, Ref: src.Commit})
			return r, nil, err
		},
		preflight: func(context.Context, FrontendEntity) error { return nil },
		publish: func(context.Context, FrontendEntity, string, string) error {
			t.Fatal("preflight published")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.got.Ref != strings.Repeat("a", 40) {
		t.Fatal("moving declaration ref reached fetcher")
	}
}

func TestSnapshotFrontendRuntimeWireRoundTrip(t *testing.T) {
	for _, runtime := range []FrontendRuntime{{Type: FrontendRuntimeFirebase, Firebase: &FirebaseRuntime{Project: "project", Site: "site", KeepAssetReleases: 8}}, {Type: FrontendRuntimeBucket, Bucket: &BucketRuntime{Bucket: "gs://bucket", KeepReleases: 0}}, {Type: FrontendRuntimeBuildOnly}} {
		f := snapshotFrontendEntity(FrontendEntity{Name: "web", Runtime: runtime})
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var got snapshotFrontendEntity
		if err = json.Unmarshal(b, &got); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if got.Runtime.Type != runtime.Type {
			t.Fatal("runtime lost")
		}
		if runtime.Firebase != nil && (got.Runtime.Firebase.Project != "project" || got.Runtime.Firebase.KeepAssetReleases != 8) {
			t.Fatal("firebase config lost")
		}
		if runtime.Bucket != nil && (got.Runtime.Bucket.Bucket != "gs://bucket" || got.Runtime.Bucket.KeepReleases != 0) {
			t.Fatal("bucket config lost")
		}
	}
}
func TestSnapshotFrontendInRepoPinnedRecipe(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "web")
	// The Git artifact is the project commit; recipe path identifies its frontend.
	for id, a := range s.Artifacts {
		src := *a.Origin.Source
		src.Subdir = ""
		a.Origin.Source = &src
		s.Artifacts[id] = a
	}
	e.Frontends[0].Source = nil
	e.Frontends[0].Path = "frontends/web"
	captureFrontendFixture(t, &s, nil, e)
	called := false
	err := snapshotPublishFrontendsWith(context.Background(), s, false, snapshotFrontendAdapters{
		resolve: func(_ context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			if src.Subdir != "" {
				t.Fatal("source identity modified")
			}
			return gitsource.Resolution{Dir: "/pinned/repo", Commit: src.Commit}, nil, nil
		},
		preflight: func(_ context.Context, f FrontendEntity) error {
			if f.Path != filepath.FromSlash("/pinned/repo/frontends/web") {
				t.Fatalf("wrong pinned recipe path %s", f.Path)
			}
			return nil
		},
		publish: func(context.Context, FrontendEntity, string, string) error { called = true; return nil },
	})
	if err != nil || !called {
		t.Fatalf("inrepo adapter: %v", err)
	}
}

func TestSnapshotFrontendBuildOnlyBeforeProvider(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "web", "docs")
	e.Frontends[1].Runtime = FrontendRuntime{Type: FrontendRuntimeBuildOnly}
	captureFrontendFixture(t, &s, nil, e)
	var order []string
	err := snapshotPublishFrontendsWith(context.Background(), s, false, snapshotFrontendAdapters{
		resolve: func(_ context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			return gitsource.Resolution{Dir: "/fake", Commit: src.Commit}, nil, nil
		},
		preflight: func(context.Context, FrontendEntity) error { return nil },
		publish: func(_ context.Context, f FrontendEntity, _ string, _ string) error {
			order = append(order, f.Name)
			return nil
		},
	})
	if err != nil || len(order) != 2 || order[0] != "docs" {
		t.Fatalf("build-only order %v %v", order, err)
	}
}

func TestSnapshotFrontendResolutionCannotOverride(t *testing.T) {
	s, e := snapshotFrontendFixture(t, "web")
	captureFrontendFixture(t, &s, nil, e)
	for _, res := range []gitsource.Resolution{{Dir: "fake", Commit: strings.Repeat("b", 40)}, {Dir: "fake", Commit: strings.Repeat("a", 40), Overridden: true}} {
		if err := snapshotPublishFrontendsWith(context.Background(), s, true, snapshotFrontendAdapters{resolve: func(context.Context, release.Source) (gitsource.Resolution, func(), error) { return res, nil, nil }}); err == nil {
			t.Fatal("unrecorded source accepted")
		}
	}
}
