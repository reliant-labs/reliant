package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/pkg/release"
)

const imageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// fakeSyft answers each syft invocation by its source scheme, records every
// call, and writes a CycloneDX 1.6 document naming the source it read.
type fakeSyft struct {
	mu    sync.Mutex
	calls [][]string
	envs  [][]string
	// fail maps a source scheme ("docker", "registry", "file") to an error.
	fail map[string]error
	// out, when set, replaces the document for every call.
	out []byte
}

func (f *fakeSyft) Run(_ context.Context, bin string, args, env []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{bin}, args...))
	f.envs = append(f.envs, env)
	f.mu.Unlock()
	src := args[1]
	scheme := src[:strings.Index(src, ":")]
	if err := f.fail[scheme]; err != nil {
		return nil, err
	}
	if f.out != nil {
		return f.out, nil
	}
	// Like syft, the document names its subject by --source-name.
	return []byte(fmt.Sprintf(`{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"source":%q,"component":{"name":%q,"version":%q}}}`,
		src, argAfter(args, "--source-name"), argAfter(args, "--source-version"))), nil
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// subjectName is the name a generated document records for its subject.
func subjectName(t *testing.T, doc []byte) string {
	t.Helper()
	var d struct {
		Metadata struct {
			Component struct {
				Name string `json:"name"`
			} `json:"component"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return d.Metadata.Component.Name
}

func (f *fakeSyft) sources() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c[2])
	}
	return out
}

func pinnedTool(context.Context) (string, string, error) { return "/opt/syft", "v1.57.2", nil }

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGenerate_EachKindReadsItsSourceAndRecordsIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := writeFile(t, dir, "cli", "a binary")
	lock := writeFile(t, dir, "package-lock.json", `{"lockfileVersion":3}`)
	fake := &fakeSyft{}
	g := &Generator{Tool: pinnedTool, Runner: fake}

	results := g.Generate(context.Background(), []Target{
		{Name: "ghcr.io/acme/api", Kind: KindImage, Ref: "ghcr.io/acme/api@" + imageDigest, Subject: imageDigest},
		{Name: "docker.io/traefik/whoami", Kind: KindRegistry, Ref: "docker.io/traefik/whoami@" + imageDigest, Subject: imageDigest},
		{Name: "cli-linux", Kind: KindBinary, Ref: bin},
		{Name: "web", Kind: KindLockfile, Ref: lock},
	})

	want := []struct{ source, scheme string }{
		{release.SBOMSourceImage, "docker:"},
		{release.SBOMSourceRegistry, "registry:"},
		{release.SBOMSourceBinary, "file:"},
		{release.SBOMSourceLockfile, "file:"},
	}
	for i, r := range results {
		if !r.Ref.HasSBOM() {
			t.Fatalf("%s: not ok: %+v", r.Target.Name, r.Ref)
		}
		if r.Ref.Source != want[i].source {
			t.Errorf("%s: source %q, want %q", r.Target.Name, r.Ref.Source, want[i].source)
		}
		if r.Ref.Format != release.SBOMFormatCycloneDXJSON || r.Ref.Generator != "syft@v1.57.2" {
			t.Errorf("%s: format/generator %q %q", r.Target.Name, r.Ref.Format, r.Ref.Generator)
		}
		if r.Ref.Digest != Digest(r.Bytes) {
			t.Errorf("%s: digest %s does not name the bytes", r.Target.Name, r.Ref.Digest)
		}
		if !strings.Contains(string(r.Bytes), want[i].scheme) {
			t.Errorf("%s: syft read %s, want a %s source", r.Target.Name, r.Bytes, want[i].scheme)
		}
	}
	// The artifact's name, never a local path, is the document's subject.
	for _, c := range fake.calls {
		if !contains(c, "--source-name") || !contains(c, "cyclonedx-json@1.6") || !contains(c, "-q") {
			t.Errorf("syft args %v miss --source-name, the pinned format or -q", c)
		}
	}
}

func TestGenerate_BuiltImageFallsBackToTheRegistry(t *testing.T) {
	t.Parallel()
	fake := &fakeSyft{fail: map[string]error{"docker": errors.New("no such image")}}
	g := &Generator{Tool: pinnedTool, Runner: fake}
	r := g.Generate(context.Background(), []Target{{Name: "api", Kind: KindImage, Ref: "r/api@" + imageDigest, Subject: imageDigest, Platform: "linux/arm64"}})[0]
	if !r.Ref.HasSBOM() || r.Ref.Source != release.SBOMSourceRegistry {
		t.Fatalf("want an ok registry-sourced SBOM, got %+v", r.Ref)
	}
	if got := fake.sources(); len(got) != 2 || got[0] != "docker:r/api@"+imageDigest || got[1] != "registry:r/api@"+imageDigest {
		t.Errorf("sources tried: %v", got)
	}
	if !contains(fake.calls[1], "--platform") {
		t.Errorf("the platform was not passed to syft: %v", fake.calls[1])
	}
}

func TestGenerate_FailuresAreUnavailableWithAReason(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name   string
		g      *Generator
		target Target
		reason string
	}{
		{"no syft", &Generator{Tool: func(context.Context) (string, string, error) {
			return "", "", errors.New("download syft: no network")
		}}, Target{Name: "a", Kind: KindRegistry, Ref: "r/a@" + imageDigest, Subject: imageDigest}, "syft is unavailable: download syft: no network"},
		{"untrusted tool cache", &Generator{Tool: func(context.Context) (string, string, error) {
			return "", "", fmt.Errorf("syft: %w", toolfetch.ErrUntrustedCache)
		}}, Target{Name: "a", Kind: KindRegistry, Ref: "r/a@" + imageDigest, Subject: imageDigest}, "syft is unavailable: syft: tool cache is writable by another user"},
		{"no generator", &Generator{}, Target{Name: "a", Kind: KindRegistry, Ref: "r/a@" + imageDigest, Subject: imageDigest}, "no SBOM generator"},
		{"registry denied", &Generator{Tool: pinnedTool, Runner: &fakeSyft{fail: map[string]error{"registry": errors.New("exit 1: 401 unauthorized")}}},
			Target{Name: "a", Kind: KindRegistry, Ref: "r/a@" + imageDigest, Subject: imageDigest}, "401 unauthorized"},
		{"missing binary", &Generator{Tool: pinnedTool, Runner: &fakeSyft{}},
			Target{Name: "cli", Kind: KindBinary, Ref: filepath.Join(dir, "absent")}, "read "},
		{"image without digest", &Generator{Tool: pinnedTool, Runner: &fakeSyft{}},
			Target{Name: "a", Kind: KindImage, Ref: "r/a:latest"}, "has no digest"},
		{"not cyclonedx 1.6", &Generator{Tool: pinnedTool, Runner: &fakeSyft{out: []byte(`{"bomFormat":"CycloneDX","specVersion":"1.7"}`)}},
			Target{Name: "a", Kind: KindRegistry, Ref: "r/a@" + imageDigest, Subject: imageDigest}, "not a CycloneDX 1.6 document"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.g.Generate(context.Background(), []Target{tc.target})[0]
			if r.Ref.HasSBOM() || r.Ref.Status != release.SBOMStatusUnavailable || r.Ref.Digest != "" || r.Bytes != nil {
				t.Fatalf("want unavailable with no digest, got %+v", r.Ref)
			}
			if !strings.Contains(r.Ref.Reason, tc.reason) {
				t.Errorf("reason %q does not say %q", r.Ref.Reason, tc.reason)
			}
		})
	}
}

func TestGenerate_CacheHitRunsNoSyft(t *testing.T) {
	t.Parallel()
	cache := &Cache{Dir: t.TempDir()}
	lock := writeFile(t, t.TempDir(), "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	targets := []Target{
		{Name: "api", Kind: KindImage, Ref: "r/api@" + imageDigest, Subject: imageDigest},
		{Name: "web", Kind: KindLockfile, Ref: lock},
	}

	first := &fakeSyft{fail: map[string]error{"docker": errors.New("not loaded")}}
	cold := (&Generator{Tool: pinnedTool, Runner: first, Cache: cache}).Generate(context.Background(), targets)
	warmSyft := &fakeSyft{}
	warm := (&Generator{Tool: pinnedTool, Runner: warmSyft, Cache: cache}).Generate(context.Background(), targets)

	if len(warmSyft.calls) != 0 {
		t.Fatalf("a cache hit ran syft: %v", warmSyft.calls)
	}
	for i := range targets {
		if cold[i].Cached || !warm[i].Cached {
			t.Errorf("%s: cached cold=%v warm=%v", targets[i].Name, cold[i].Cached, warm[i].Cached)
		}
		if warm[i].Ref != cold[i].Ref || string(warm[i].Bytes) != string(cold[i].Bytes) {
			t.Errorf("%s: cached ref %+v differs from generated %+v", targets[i].Name, warm[i].Ref, cold[i].Ref)
		}
	}
	// The source that answered is part of the entry: the image came from
	// the registry the first time, and the hit says so.
	if warm[0].Ref.Source != release.SBOMSourceRegistry {
		t.Errorf("cached source %q, want registry", warm[0].Ref.Source)
	}

	// Another syft version, another platform, or changed lockfile bytes is a
	// different key: syft runs again.
	newer := &fakeSyft{}
	(&Generator{Tool: func(context.Context) (string, string, error) { return "/opt/syft", "1.58.0", nil }, Runner: newer, Cache: cache}).
		Generate(context.Background(), targets)
	if len(newer.calls) != 2 {
		t.Errorf("a newer syft reused an older one's SBOMs: %d calls", len(newer.calls))
	}
	other := &fakeSyft{}
	if err := os.WriteFile(lock, []byte("lockfileVersion: '9.0'\nchanged: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arm := targets[0]
	arm.Platform = "linux/arm64"
	(&Generator{Tool: pinnedTool, Runner: other, Cache: cache}).Generate(context.Background(), []Target{arm, targets[1]})
	if len(other.calls) != 2 {
		t.Errorf("a new platform or lockfile hit the cache: %d calls", len(other.calls))
	}
}

func TestCache_DamagedEntryIsAMiss(t *testing.T) {
	t.Parallel()
	cache := &Cache{Dir: t.TempDir()}
	key := CacheKey{Generator: "syft@v1.57.2", Subject: imageDigest, SourceName: "ghcr.io/acme/api"}
	doc := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)
	if err := cache.Put(key, release.SBOMSourceImage, doc); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cache.Get(key); !ok {
		t.Fatal("a stored entry missed")
	}
	stem, _ := cache.stem(key)
	if err := os.WriteFile(stem+".cdx.json", []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cache.Get(key); ok {
		t.Error("bytes that no longer hash to their digest were served")
	}
	var nilCache *Cache
	if _, _, ok := nilCache.Get(key); ok || nilCache.Put(key, "image", doc) != nil {
		t.Error("a nil cache must miss and accept writes silently")
	}
}

// The document records its subject's name (--source-name), so the name is
// part of what the cache is keyed on: identical bytes under two names are two
// documents, each naming its own artifact. The same name again, in another
// project or release, is still one syft run.
func TestGenerate_CacheKeysOnTheSourceName(t *testing.T) {
	t.Parallel()
	cache := &Cache{Dir: t.TempDir()}
	targets := []Target{
		{Name: "ghcr.io/acme/api", Kind: KindRegistry, Ref: "ghcr.io/acme/api@" + imageDigest, Subject: imageDigest},
		{Name: "registry.example.com/mirror/api", Kind: KindRegistry, Ref: "registry.example.com/mirror/api@" + imageDigest, Subject: imageDigest},
	}
	first := &fakeSyft{}
	g := &Generator{Tool: pinnedTool, Runner: first, Cache: cache, Concurrency: 1}
	cold := g.Generate(context.Background(), targets)
	if len(first.calls) != 2 {
		t.Fatalf("two names over one digest ran syft %d time(s), want 2", len(first.calls))
	}
	for _, r := range cold {
		if got := subjectName(t, r.Bytes); got != r.Target.Name {
			t.Errorf("%s: SBOM names %q", r.Target.Name, got)
		}
	}
	if cold[0].Ref.Digest == cold[1].Ref.Digest {
		t.Errorf("two different documents share digest %s", cold[0].Ref.Digest)
	}

	again := &fakeSyft{}
	warm := (&Generator{Tool: pinnedTool, Runner: again, Cache: cache}).Generate(context.Background(), targets)
	if len(again.calls) != 0 {
		t.Errorf("the same names over the same digest ran syft again: %v", again.calls)
	}
	for i, r := range warm {
		if !r.Cached || r.Ref.Digest != cold[i].Ref.Digest || subjectName(t, r.Bytes) != r.Target.Name {
			t.Errorf("%s: warm %+v names %q, want the cold document", r.Target.Name, r.Ref, subjectName(t, r.Bytes))
		}
	}

	// A version the document records is part of the key too.
	versioned := targets[0]
	versioned.Version = "v2"
	other := &fakeSyft{}
	(&Generator{Tool: pinnedTool, Runner: other, Cache: cache}).Generate(context.Background(), []Target{versioned})
	if len(other.calls) != 1 {
		t.Errorf("a new source version reused another version's document: %d call(s)", len(other.calls))
	}
}

func TestGenerate_BoundedConcurrencyKeepsOrder(t *testing.T) {
	t.Parallel()
	var running, peak atomic.Int32
	runner := runnerFunc(func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return []byte(fmt.Sprintf(`{"bomFormat":"CycloneDX","specVersion":"1.6","name":%q}`, args[1])), nil
	})
	var targets []Target
	for i := 0; i < 9; i++ {
		d := fmt.Sprintf("sha256:%064d", i)
		targets = append(targets, Target{Name: fmt.Sprint(i), Kind: KindRegistry, Ref: "r/x@" + d, Subject: d})
	}
	results := (&Generator{Tool: pinnedTool, Runner: runner, Concurrency: 2}).Generate(context.Background(), targets)
	if p := peak.Load(); p > 2 {
		t.Errorf("%d syft runs at once, bound is 2", p)
	}
	for i, r := range results {
		if r.Target.Name != fmt.Sprint(i) || !strings.Contains(string(r.Bytes), targets[i].Ref) {
			t.Errorf("result %d is for %s", i, r.Target.Name)
		}
	}
}

// Timeout bounds one target's syft run: a hung syft makes that target
// unavailable, saying why, while the next target is still described.
func TestGenerate_TimeoutBoundsOneTarget(t *testing.T) {
	t.Parallel()
	runner := runnerFunc(func(ctx context.Context, _ string, args, _ []string) ([]byte, error) {
		if strings.Contains(args[1], "hung") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`), nil
	})
	hung, ok := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("2", 64)
	results := (&Generator{Tool: pinnedTool, Runner: runner, Timeout: 20 * time.Millisecond}).Generate(context.Background(), []Target{
		{Name: "hung", Kind: KindRegistry, Ref: "r/hung@" + hung, Subject: hung},
		{Name: "ok", Kind: KindRegistry, Ref: "r/ok@" + ok, Subject: ok},
	})
	if r := results[0].Ref; r.Status != release.SBOMStatusUnavailable || !strings.Contains(r.Reason, context.DeadlineExceeded.Error()) {
		t.Errorf("hung target: %+v, want unavailable with the deadline", r)
	}
	if !results[1].Ref.HasSBOM() {
		t.Errorf("ok target: %+v, want an SBOM", results[1].Ref)
	}
}

func TestGeneratorName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"v1.57.2": "syft@v1.57.2", "1.57.2": "syft@v1.57.2"} {
		if got := GeneratorName(in); got != want {
			t.Errorf("GeneratorName(%q) = %q, want %q", in, got, want)
		}
	}
}

type runnerFunc func(ctx context.Context, bin string, args, env []string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, bin string, args, env []string) ([]byte, error) {
	return f(ctx, bin, args, env)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
