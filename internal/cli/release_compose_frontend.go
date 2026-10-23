package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/release"
)

const snapshotFrontendAssetPath = "forge/frontend-declarations.json"
const snapshotFrontendSchema = "forge.dev/frontend-declarations/v1"

// FrontendRuntime has a render-oriented decoder but no symmetric encoder.
// Preserve its existing flat runtime wire shape without changing that shared type.
type snapshotFrontendEntity FrontendEntity

func (f snapshotFrontendEntity) MarshalJSON() ([]byte, error) {
	type entity FrontendEntity
	r := FrontendEntity(f).Runtime
	runtime := map[string]any{"type": r.Type}
	switch r.Type {
	case FrontendRuntimeFirebase:
		if r.Firebase == nil {
			return nil, fmt.Errorf("missing Firebase runtime")
		}
		b, _ := json.Marshal(r.Firebase)
		if err := json.Unmarshal(b, &runtime); err != nil {
			return nil, err
		}
		runtime["type"] = r.Type
	case FrontendRuntimeBucket:
		if r.Bucket == nil {
			return nil, fmt.Errorf("missing bucket runtime")
		}
		b, _ := json.Marshal(r.Bucket)
		if err := json.Unmarshal(b, &runtime); err != nil {
			return nil, err
		}
		runtime["type"] = r.Type
	}
	return json.Marshal(struct {
		*entity
		Runtime map[string]any `json:"runtime"`
	}{(*entity)(&f), runtime})
}
func (f *snapshotFrontendEntity) UnmarshalJSON(b []byte) error {
	var e FrontendEntity
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return err
	}
	*f = snapshotFrontendEntity(e)
	return nil
}

type snapshotFrontend struct {
	Consumer        release.ConsumerKey    `json:"consumer"`
	Source          release.Source         `json:"source"`
	CodeSubdir      string                 `json:"code_subdir,omitempty"`
	Entity          snapshotFrontendEntity `json:"entity"`
	RuntimeConfigJS string                 `json:"runtime_config_js"`
	Release         string                 `json:"release"`
}
type snapshotFrontendDeclarations struct {
	Schema    string             `json:"schema"`
	Frontends []snapshotFrontend `json:"frontends"`
}

func snapshotFrontendEntries(s release.Snapshot) (snapshotFrontendDeclarations, error) {
	doc := snapshotFrontendDeclarations{Schema: snapshotFrontendSchema}
	found := false
	for _, a := range s.Assets {
		if a.Path != snapshotFrontendAssetPath {
			continue
		}
		if found {
			return doc, fmt.Errorf("duplicate frozen frontend asset")
		}
		found = true
		if a.Digest != release.SpecDigest(a.Data) {
			return doc, fmt.Errorf("frozen frontend asset digest mismatch")
		}
		if len(a.Data) > 4<<20 {
			return doc, fmt.Errorf("frozen frontend asset exceeds limit")
		}
		d := json.NewDecoder(bytes.NewReader(a.Data))
		d.DisallowUnknownFields()
		if err := d.Decode(&doc); err != nil {
			return doc, err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return doc, fmt.Errorf("trailing frozen frontend data")
		}
		if doc.Schema != snapshotFrontendSchema {
			return doc, fmt.Errorf("unsupported frozen frontend schema")
		}
	}
	seen := map[release.ConsumerKey]bool{}
	for _, f := range doc.Frontends {
		if seen[f.Consumer] {
			return doc, fmt.Errorf("duplicate frontend %s", f.Consumer.Workload)
		}
		seen[f.Consumer] = true
		if err := validateSnapshotFrontend(s, f); err != nil {
			return doc, err
		}
	}
	for _, b := range s.Bindings {
		if b.Slot == "frontend" && !seen[b.Consumer] {
			return doc, fmt.Errorf("snapshot lacks frozen frontend %s", b.Consumer.Workload)
		}
	}
	for _, c := range s.Consumers {
		for _, slot := range c.Slots {
			if slot == "frontend" && !seen[c.Key] {
				return doc, fmt.Errorf("frontend consumer has no frozen declaration")
			}
		}
	}
	return doc, nil
}

func snapshotFrontendSource(s release.Snapshot, k release.ConsumerKey) (release.Source, string, error) {
	var result release.Source
	version := ""
	count := 0
	for _, b := range s.Bindings {
		if b.Consumer != k || b.Slot != "frontend" {
			continue
		}
		count++
		a, ok := s.Artifacts[b.Artifact]
		if !ok || a.Kind != release.KindGit || a.Origin.Source == nil || a.Content != a.Origin.Source.Commit || a.Location != a.Origin.Source.Repo {
			return result, "", fmt.Errorf("frontend %s lacks exact Git artifact origin", k.Workload)
		}
		result = *a.Origin.Source
		version = a.Origin.Release
	}
	if count != 1 || !release.ValidObjectID(result.Commit) {
		return result, "", fmt.Errorf("frontend %s lacks one resolved source commit", k.Workload)
	}
	return result, version, nil
}
func snapshotSafeSubdir(p string) bool {
	return p == "" || (p != "." && !strings.Contains(p, "\\") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../"))
}
func validateSnapshotFrontend(s release.Snapshot, f snapshotFrontend) error {
	src, version, err := snapshotFrontendSource(s, f.Consumer)
	if err != nil {
		return err
	}
	e := FrontendEntity(f.Entity)
	if f.Consumer.Cluster != "frontend" || e.Name != f.Consumer.Workload || f.Source != src || f.Release != version || e.Path != "" || e.SourceOverridden || !snapshotSafeSubdir(src.Subdir) || !snapshotSafeSubdir(f.CodeSubdir) || !snapshotSafeSubdir(e.PublicDir) || !snapshotSafeSubdir(e.AssetDir) || e.EnvFile != "" {
		return fmt.Errorf("frozen frontend %s does not match source binding", f.Consumer.Workload)
	}
	if u, err := url.Parse(src.Repo); err == nil {
		if u.RawQuery != "" || u.Fragment != "" || (u.User != nil && u.User.String() != "git") {
			return fmt.Errorf("frontend repository must not contain credentials or mutable URL parameters")
		}
	}
	if len(e.Bundle) > 0 {
		return fmt.Errorf("snapshot frontend %s has unfrozen bundle directories; explicit pinned bundle sources required", e.Name)
	}
	if err := checkDeployableFrontendMock(e); err != nil {
		return err
	}
	for _, v := range e.EnvVars {
		public := strings.HasPrefix(v.Name, "VITE_") || strings.HasPrefix(v.Name, "NEXT_PUBLIC_") || strings.HasPrefix(v.Name, "EXPO_PUBLIC_") || v.Name == "FRONTEND_PORT" || v.Name == "PORT" || v.Name == "NODE_ENV" || v.Name == "NEXT_TELEMETRY_DISABLED"
		if v.SecretRef != "" || v.ConfigMapRef != "" || !public {
			return fmt.Errorf("snapshot frontend %s has unresolved or non-public build environment %s", e.Name, v.Name)
		}
	}
	switch e.Runtime.Type {
	case FrontendRuntimeFirebase:
		if e.Runtime.Firebase == nil || e.Runtime.Firebase.Project == "" || e.Runtime.Firebase.Site == "" {
			return fmt.Errorf("invalid frozen Firebase target")
		}
	case FrontendRuntimeBucket:
		if e.Runtime.Bucket == nil || e.Runtime.Bucket.Bucket == "" {
			return fmt.Errorf("invalid frozen bucket target")
		}
	case FrontendRuntimeBuildOnly:
	default:
		return fmt.Errorf("snapshot frontend %s runtime %q lacks a frozen publication adapter", e.Name, e.Runtime.Type)
	}
	if e.Source != nil && (e.Source.Repo != src.Repo || e.Source.Ref != src.Ref || e.Source.Subdir != src.Subdir || f.CodeSubdir != "") {
		return fmt.Errorf("frozen frontend source declaration differs from binding")
	}
	if e.Source == nil && f.CodeSubdir == "" {
		return fmt.Errorf("in-repo frontend is missing frozen code subdirectory")
	}
	if err := (gitsource.Source{Repo: src.Repo, Ref: src.Commit, Subdir: src.Subdir}).Validate(); err != nil {
		return err
	}
	return nil
}

// Called only while constructing a candidate; never from deploy/preflight.
func captureSnapshotFrontends(ctx context.Context, s *release.Snapshot, base *release.Snapshot, e *KCLEntities, projectDir, env string) error {
	return captureSnapshotFrontendsWith(ctx, s, base, e, projectDir, env, renderFrontendRuntimeDocsWith)
}
func captureSnapshotFrontendsWith(_ context.Context, s *release.Snapshot, base *release.Snapshot, e *KCLEntities, projectDir, env string, render func(string, string, map[string]map[string]string) (map[string]string, error)) error {
	if s == nil {
		return fmt.Errorf("missing frontend snapshot")
	}
	selected := map[release.ConsumerKey]bool{}
	for _, k := range s.Selected {
		selected[k] = true
	}
	previous := map[release.ConsumerKey]snapshotFrontend{}
	if base != nil {
		doc, err := snapshotFrontendEntries(*base)
		if err != nil {
			return err
		}
		for _, f := range doc.Frontends {
			previous[f.Consumer] = f
		}
	}
	entities := map[string]FrontendEntity{}
	if e != nil {
		for _, f := range e.Frontends {
			if _, ok := entities[f.Name]; ok {
				return fmt.Errorf("duplicate frontend declaration %s", f.Name)
			}
			entities[f.Name] = f
		}
	}
	var docs map[string]string
	doc := snapshotFrontendDeclarations{Schema: snapshotFrontendSchema}
	for _, b := range s.Bindings {
		if b.Slot != "frontend" {
			continue
		}
		if !selected[b.Consumer] {
			f, ok := previous[b.Consumer]
			if !ok {
				return fmt.Errorf("base lacks frozen frontend %s", b.Consumer.Workload)
			}
			doc.Frontends = append(doc.Frontends, f)
			continue
		}
		f, ok := entities[b.Consumer.Workload]
		if !ok {
			return fmt.Errorf("selected frontend declaration missing")
		}
		src, version, err := snapshotFrontendSource(*s, b.Consumer)
		if err != nil {
			return err
		}
		if f.SourceOverridden {
			return fmt.Errorf("overridden frontend cannot be frozen")
		}
		if f.Source != nil {
			if f.Source.Repo != src.Repo || f.Source.Subdir != src.Subdir || f.SourceCommit != src.Commit {
				return fmt.Errorf("frontend resolved source differs from artifact")
			}
		} else {
			if !snapshotSafeSubdir(f.Path) || f.Path == "" {
				return fmt.Errorf("in-repo frontend requires a safe repo-relative path")
			}
			// The artifact's source originally names the project root. The frontend
			// path is a frozen recipe subdirectory, not a different source identity.
		}
		if docs == nil {
			if err := checkDeployableFrontendMock(f); err != nil {
				return err
			}
			docs, err = render(projectDir, env, frontendRuntimeOverlays(e))
			if err != nil {
				return err
			}
			if docs == nil {
				docs = map[string]string{}
			}
		}
		subdir := src.Subdir
		if f.Source == nil {
			subdir = f.Path
		}
		f.Path = ""
		f.SourceCommit = ""
		f.Release = ""
		f.SourceOverridden = false
		// A project-local frontend's recipe path is relative to the pinned
		// project checkout; it never resolves against the caller's current cwd.
		codeSubdir := ""
		if f.Source == nil {
			codeSubdir = subdir
		}
		if f.Source != nil {
			copy := *f.Source
			// Keep the original declared ref for build attribution only. The
			// resolver below ALWAYS fetches the artifact's recorded Commit.
			f.Source = &copy
		}
		entry := snapshotFrontend{Consumer: b.Consumer, Source: src, CodeSubdir: codeSubdir, Entity: snapshotFrontendEntity(f), RuntimeConfigJS: docs[f.Name], Release: version}
		if err := validateSnapshotFrontend(*s, entry); err != nil {
			return err
		}
		doc.Frontends = append(doc.Frontends, entry)
	}
	if len(doc.Frontends) == 0 {
		return nil
	}
	sort.Slice(doc.Frontends, func(i, j int) bool { return doc.Frontends[i].Consumer.Workload < doc.Frontends[j].Consumer.Workload })
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	assets := make([]release.SnapshotAsset, 0, len(s.Assets)+1)
	for _, a := range s.Assets {
		if a.Path != snapshotFrontendAssetPath {
			assets = append(assets, a)
		}
	}
	assets = append(assets, release.SnapshotAsset{Path: snapshotFrontendAssetPath, Data: data, Digest: release.SpecDigest(data)})
	s.Assets = assets
	return nil
}

type snapshotFrontendAdapters struct {
	resolve   func(context.Context, release.Source) (gitsource.Resolution, func(), error)
	preflight func(context.Context, FrontendEntity) error
	publish   func(context.Context, FrontendEntity, string, string) error
}

func snapshotPublishFrontends(ctx context.Context, s release.Snapshot, dryRun bool) error {
	return snapshotPublishFrontendsWith(ctx, s, dryRun, snapshotFrontendAdapters{
		resolve: func(ctx context.Context, src release.Source) (gitsource.Resolution, func(), error) {
			tmp, err := os.MkdirTemp("", "forge-snapshot-frontend-*")
			if err != nil {
				return gitsource.Resolution{}, nil, err
			}
			cleanup := func() { _ = os.RemoveAll(tmp) }
			resolver, err := gitsource.NewResolver(tmp, gitsource.WithCacheRoot(filepath.Join(tmp, "sources")), gitsource.WithOverrides(map[string]string{}))
			if err != nil {
				cleanup()
				return gitsource.Resolution{}, nil, err
			}
			// Fetch the complete pinned repo. In-repo code paths and symlink
			// checks below are then relative to that immutable checkout root.
			res, err := resolver.Resolve(ctx, gitsource.Source{Repo: src.Repo, Ref: src.Commit})
			if err != nil {
				cleanup()
				return res, nil, err
			}
			return res, cleanup, nil
		},
		preflight: func(_ context.Context, f FrontendEntity) error {
			real, err := filepath.EvalSymlinks(f.Path)
			if err != nil {
				return err
			}
			if real != filepath.Clean(f.Path) {
				return fmt.Errorf("frontend code directory contains symlink indirection")
			}
			for _, relative := range []string{f.PublicDir, f.AssetDir} {
				if relative != "" {
					parts := strings.Split(relative, "/")
					p := f.Path
					for _, part := range parts {
						p = filepath.Join(p, part)
						info, e := os.Lstat(p)
						if os.IsNotExist(e) {
							break
						}
						if e != nil {
							return e
						}
						if info.Mode()&os.ModeSymlink != 0 {
							return fmt.Errorf("frontend output path traverses symlink")
						}
					}
				}
			}
			if err := checkDeployableFrontendMock(f); err != nil {
				return err
			}
			runner := f.DevRunner
			if runner == "" {
				runner = "npm"
			}
			if runner != "npm" && runner != "pnpm" && runner != "yarn" {
				return fmt.Errorf("unsupported frozen frontend runner")
			}
			if _, err := exec.LookPath(runner); err != nil {
				return err
			}
			if err := gateFrontendEnvFiles(f.Path, []string{f.Path}); err != nil {
				return err
			}
			if f.Runtime.Type == FrontendRuntimeFirebase {
				_, err := exec.LookPath(deploytarget.FirebaseCLI)
				return err
			}
			return nil
		},
		publish: func(ctx context.Context, f FrontendEntity, js, env string) error {
			switch f.Runtime.Type {
			case FrontendRuntimeBuildOnly:
				return (deploytarget.FirebaseProvider{ProjectDir: f.Path}).BuildOnly(ctx, []deploytarget.BuildOnlyFrontend{frontendToBuildOnly(f)}, false)
			case FrontendRuntimeFirebase:
				fb := frontendToFirebase(f)
				fb.RuntimeConfigJS = js
				fb.SourcemapPublisher = sourcemapPublisherForEnv(ctx, env)
				return (deploytarget.FirebaseProvider{ProjectDir: f.Path}).Deploy(ctx, deploytarget.ServiceGroup{Env: env, Frontends: []deploytarget.FirebaseFrontend{fb}})
			case FrontendRuntimeBucket:
				ss := frontendToStaticSite(f)
				ss.RuntimeConfigJS = js
				ss.SourcemapPublisher = sourcemapPublisherForEnv(ctx, env)
				return (deploytarget.StaticSiteProvider{ProjectDir: f.Path}).Deploy(ctx, deploytarget.ServiceGroup{Env: env, StaticSites: []deploytarget.StaticSiteFrontend{ss}})
			}
			return fmt.Errorf("unsupported frozen frontend runtime")
		},
	})
}
func snapshotPublishFrontendsWith(ctx context.Context, s release.Snapshot, dryRun bool, a snapshotFrontendAdapters) error {
	doc, err := snapshotFrontendEntries(s)
	if err != nil {
		return err
	}
	selected := map[release.ConsumerKey]bool{}
	for _, k := range s.Selected {
		selected[k] = true
	}
	var prepared []struct {
		f  FrontendEntity
		js string
	}
	var cleanups []func()
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()
	for _, entry := range doc.Frontends {
		if !selected[entry.Consumer] {
			continue
		}
		res, cleanup, err := a.resolve(ctx, entry.Source)
		if err != nil {
			return err
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
		if res.Overridden || res.Commit != entry.Source.Commit || res.Dir == "" {
			return fmt.Errorf("frontend exact Git resolution mismatch")
		}
		f := FrontendEntity(entry.Entity)
		f.Path = filepath.Join(res.Dir, filepath.FromSlash(entry.Source.Subdir), filepath.FromSlash(entry.CodeSubdir))
		f.SourceCommit = entry.Source.Commit
		f.Release = entry.Release
		f.Project = s.Project
		if err := a.preflight(ctx, f); err != nil {
			return err
		}
		prepared = append(prepared, struct {
			f  FrontendEntity
			js string
		}{f, entry.RuntimeConfigJS})
	}
	if dryRun {
		return nil
	}
	// Every selected frontend preflights before the first provider side effect.
	sort.SliceStable(prepared, func(i, j int) bool {
		return prepared[i].f.Runtime.Type == FrontendRuntimeBuildOnly && prepared[j].f.Runtime.Type != FrontendRuntimeBuildOnly
	})
	for _, p := range prepared {
		if err := a.publish(ctx, p.f, p.js, s.Environment); err != nil {
			return err
		}
	}
	return nil
}
