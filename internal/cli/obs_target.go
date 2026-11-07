package cli

// What `forge obs` needs to know about an env before its first request: which
// control plane it declares, which env var names the credential, and the few
// facts backend resolution reads. All of it comes from the env's rendered KCL,
// and a render of a large env is tens of seconds — so a `--backend cloud` read
// that the control plane was going to refuse with a 403 used to spend those
// seconds first (and, in openObsCloud, spend them a second time).
//
// The answer is remembered in .forge/state/obs-target-<env>.json, stamped with
// a fingerprint of everything that decides it: the stat of every file under
// deploy/kcl, forge.yaml and this forge build. A matching stamp is the same
// declaration the render would produce, so the render is skipped; any edit
// changes the stamp and the next command renders (once) and rewrites the file.
// The cache can only cost a render, never a wrong answer: a fingerprint that
// cannot be computed (render options, a fixture render, an unreadable tree)
// disables it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/release"
)

const (
	obsTargetVersion = 3
	// obsTargetMaxAge bounds how long a stamp is trusted even when every input
	// it names is unchanged: a main.k may read a file outside deploy/kcl, which
	// the stamp cannot see.
	obsTargetMaxAge = 6 * time.Hour
	// obsTargetMaxFiles bounds the fingerprint walk: a deploy/kcl tree this
	// large is not a declaration, and hashing it would cost what it saves.
	obsTargetMaxFiles = 4000
)

// obsTarget is an env's observability-relevant declaration.
type obsTarget struct {
	Facts    obsbackend.Facts
	TokenEnv string
}

// Declaration is the control-plane declaration the target carries; nil when the
// env declares none.
func (t obsTarget) Declaration() *cloud.Declaration {
	if strings.TrimSpace(t.Facts.ControlPlane) == "" {
		return nil
	}
	return &cloud.Declaration{Endpoint: t.Facts.ControlPlane, TokenEnv: t.TokenEnv}
}

// obsTargetFromEntities reads the target out of a render.
func obsTargetFromEntities(env string, e *KCLEntities) obsTarget {
	f := obsbackend.Facts{Env: env, HasHosted: e.HasHosted(), HasLocal: e.HasLocal(), ReleaseHealth: e.ReleaseHealth,
		LocalLifecycle: e.Lifecycle == lifecycleLocal || e.Lifecycle == lifecycleEphemeral}
	for _, w := range e.Workloads {
		if w.OnRuntime(RuntimeCluster) {
			f.HasCluster = true
		}
	}
	if o := e.Observability; o != nil {
		f.Declared, f.Enabled = true, o.Enabled
		if d := o.Destination; d != nil {
			f.Destination = &obsbackend.Destination{Endpoint: d.Endpoint, TokenFromControlPlane: d.TokenFromControlPlane}
		}
	}
	t := obsTarget{Facts: f}
	if e.ControlPlane != nil {
		t.Facts.ControlPlane = e.ControlPlane.Endpoint
		t.TokenEnv = e.ControlPlane.TokenEnv
	}
	return t
}

// obsTargetFile is the on-disk record. Its own type, so the cache format does
// not move when obsbackend.Facts grows a field.
type obsTargetFile struct {
	Version        int                         `json:"version"`
	At             time.Time                   `json:"at"`
	Fingerprint    string                      `json:"fingerprint"`
	Env            string                      `json:"env"`
	Declared       bool                        `json:"declared,omitempty"`
	Enabled        bool                        `json:"enabled,omitempty"`
	Destination    *obsbackend.Destination     `json:"destination,omitempty"`
	HasHosted      bool                        `json:"has_hosted,omitempty"`
	HasLocal       bool                        `json:"has_local,omitempty"`
	HasCluster     bool                        `json:"has_cluster,omitempty"`
	LocalLifecycle bool                        `json:"local_lifecycle,omitempty"`
	ControlPlane   string                      `json:"control_plane,omitempty"`
	TokenEnv       string                      `json:"token_env,omitempty"`
	Health         *release.ShapeReleaseHealth `json:"release_health,omitempty"`
}

func obsTargetPath(projectDir, env string) string {
	return filepath.Join(projectDir, ".forge", "state", "obs-target-"+env+".json")
}

// obsTargetFingerprint stamps the inputs that decide an env's target. "" means
// they cannot be stamped, and the cache must not be used.
func obsTargetFingerprint(projectDir, env string) string {
	if os.Getenv("FORGE_KCL_RENDER_FIXTURE") != "" || len(activeRenderOptionDArgs()) > 0 {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "v%d\x00%s\x00%s\x00", obsTargetVersion, env, buildinfo.Identity())
	stat := func(path, rel string) bool {
		fi, err := os.Stat(path)
		if err != nil {
			return false
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, fi.Size(), fi.ModTime().UnixNano())
		return true
	}
	if !stat(filepath.Join(projectDir, "forge.yaml"), "forge.yaml") {
		return ""
	}
	// kcl.mod's forge_projects resolve through go.mod's pins.
	stat(filepath.Join(projectDir, "go.mod"), "go.mod")
	stat(filepath.Join(projectDir, "go.sum"), "go.sum")
	root := filepath.Join(projectDir, "deploy", "kcl")
	// A path dependency (the sibling forge checkout) is KCL the stamp cannot see.
	if mod, err := os.ReadFile(filepath.Join(root, "kcl.mod")); err != nil || kclModHasPathDependency(string(mod)) {
		return ""
	}
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if files++; files > obsTargetMaxFiles {
			return fmt.Errorf("deploy/kcl has more than %d files", obsTargetMaxFiles)
		}
		rel, rerr := filepath.Rel(projectDir, path)
		if rerr != nil || !stat(path, filepath.ToSlash(rel)) {
			return fmt.Errorf("cannot stat %s", path)
		}
		return nil
	})
	if err != nil || files == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

var kclPathDependency = regexp.MustCompile(`(^|[\s{,])path\s*=`)

// kclModHasPathDependency reports whether kcl.mod names a dependency by local
// path.
func kclModHasPathDependency(mod string) bool {
	for _, line := range strings.Split(mod, "\n") {
		if line = strings.TrimSpace(line); !strings.HasPrefix(line, "#") && kclPathDependency.MatchString(line) {
			return true
		}
	}
	return false
}

func obsTargetCacheable(env string) bool {
	return env != "" && !strings.ContainsAny(env, `/\`) && env != "." && env != ".."
}

func readObsTarget(projectDir, env, fingerprint string) (obsTarget, bool) {
	if fingerprint == "" || !obsTargetCacheable(env) {
		return obsTarget{}, false
	}
	data, err := os.ReadFile(obsTargetPath(projectDir, env))
	if err != nil {
		return obsTarget{}, false
	}
	var f obsTargetFile
	if json.Unmarshal(data, &f) != nil || f.Version != obsTargetVersion || f.Fingerprint != fingerprint || f.Env != env {
		return obsTarget{}, false
	}
	if age := time.Since(f.At); age < 0 || age > obsTargetMaxAge {
		return obsTarget{}, false
	}
	return obsTarget{
		TokenEnv: f.TokenEnv,
		Facts: obsbackend.Facts{
			Env: env, Declared: f.Declared, Enabled: f.Enabled, Destination: f.Destination,
			HasHosted: f.HasHosted, HasLocal: f.HasLocal, HasCluster: f.HasCluster, LocalLifecycle: f.LocalLifecycle, ControlPlane: f.ControlPlane, ReleaseHealth: f.Health,
		},
	}, true
}

// writeObsTarget records t. Best-effort: a cache that cannot be written costs
// the next command a render, and must never fail this one.
func writeObsTarget(projectDir, env, fingerprint string, t obsTarget) {
	if fingerprint == "" || !obsTargetCacheable(env) {
		return
	}
	data, err := json.Marshal(obsTargetFile{
		Version: obsTargetVersion, At: time.Now().UTC(), Fingerprint: fingerprint, Env: env,
		Declared: t.Facts.Declared, Enabled: t.Facts.Enabled, Destination: t.Facts.Destination,
		HasHosted: t.Facts.HasHosted, HasLocal: t.Facts.HasLocal, HasCluster: t.Facts.HasCluster, LocalLifecycle: t.Facts.LocalLifecycle, ControlPlane: t.Facts.ControlPlane, TokenEnv: t.TokenEnv,
		Health: t.Facts.ReleaseHealth,
	})
	if err != nil {
		return
	}
	path := obsTargetPath(projectDir, env)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = atomicfile.WriteFile(path, data, 0o644)
}

// obsTargetLast shares one process's answers, keyed by the fingerprint they were
// read under.
var (
	obsTargetMu    sync.Mutex
	obsTargetLast  = map[string]obsTarget{}
	obsTargetKeyOf = func(projectDir, env, fingerprint string) string {
		return projectDir + "\x00" + env + "\x00" + fingerprint
	}
)

// resolveObsTarget returns env's target: from the stamped cache when the
// declaration has not changed, else by rendering it (and stamping the result).
// Within a process the answer is shared, so a verb that asks twice — backend
// resolution, then opening the backend — renders at most once.
func resolveObsTarget(ctx context.Context, env string) (obsTarget, error) {
	projectDir := projectDirForKCL()
	fp := obsTargetFingerprint(projectDir, env)
	key := obsTargetKeyOf(projectDir, env, fp)
	if fp != "" {
		obsTargetMu.Lock()
		t, ok := obsTargetLast[key]
		obsTargetMu.Unlock()
		if ok {
			return t, nil
		}
		if t, ok := readObsTarget(projectDir, env, fp); ok {
			remember(key, t)
			return t, nil
		}
	}
	e, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return obsTarget{}, err
	}
	t := obsTargetFromEntities(env, e)
	if fp != "" {
		writeObsTarget(projectDir, env, fp, t)
		remember(key, t)
	}
	return t, nil
}

func remember(key string, t obsTarget) {
	obsTargetMu.Lock()
	obsTargetLast[key] = t
	obsTargetMu.Unlock()
}
