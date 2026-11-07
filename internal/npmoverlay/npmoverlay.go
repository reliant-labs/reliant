// Package npmoverlay serves an npm registry that is the public one plus
// packages packed from a local checkout, so a test installs the code it is
// testing rather than the last release.
//
// # Why forge needs it
//
// A scaffolded frontend depends on @reliantlabs/forge-web-runtime by a
// published semver range, and forge raises that range in the same pull request
// that ships the runtime it needs (web-runtime/package.json, the range in
// internal/generator). The version is published only after the merge, when
// someone pushes the runtime's tag. Until then the range names a version the
// registry does not have, so every e2e test and the `forge project new` smoke
// failed `npm install` with ETARGET — on exactly the pull requests that most
// needed them — and, between releases, passed against the PREVIOUS runtime,
// so they never tested the one the pull request ships.
//
// # Why a registry, and not a link or a file: spec
//
// The dev bridge (generator.EnsureDevWebRuntimeLink) links the checkout as an
// npm workspace member. It is off under CI on purpose: the member brings its
// own node_modules as a second resolution root, so tsc saw two copies of
// @connectrpc/connect — a resolution graph no user ever gets (see
// internal/buildinfo/ci.go). Rewriting the dependency to `file:<tgz>` would
// edit the tracked manifest the tests check, never exercise the range, and
// fight generate's floor reconciliation, which owns that line.
//
// A registry changes neither. The scaffold keeps its published range, npm
// resolves it exactly as it does for a user — semver, peers, a lockfile entry
// with an integrity hash — and the tarball it downloads is `npm pack` of the
// checkout, byte for byte what `npm publish` would upload. If the range does
// not admit the checkout's version, the install fails here, as it would after
// the release.
//
// # How it is wired
//
// Only the scopes of the overlaid packages are routed here, through an npm
// GLOBAL config file named by npm_config_globalconfig (Env). The global file
// is the one level nobody configures: the user's ~/.npmrc, its auth and its
// mirrors stay in force, and the variable name is an ordinary one, so a CI
// step can export it. Within a routed scope, a packument for an overlaid
// package is the upstream one with the local version added (or replacing a
// published one of the same number); anything else in the scope is proxied
// unchanged. Upstream tarball URLs stay absolute, so they download from the
// real registry.
//
// pnpm reads none of that: it ignores npm_config_globalconfig and every
// per-scope npm_config_* variable (measured, pnpm 12), and takes registry
// routes only from .npmrc files and its own config. So Env also names a pnpm
// USER config (pnpm_config_userconfig, which npm does not read): the user's
// own .npmrc, auth and all, with the scope routes appended — the same
// "add, take nothing away" rule as the npm file. And because pnpm by default
// prefers a version published at least a day ago (minimumReleaseAge), and the
// overlay dates its local version now, Env exempts the overlaid packages from
// that rule; otherwise a range admitting both would quietly install the last
// release, the very thing this package exists to prevent.
//
// It is test plumbing, no part of the forge binary: the e2e suite starts one
// per test process (internal/cli, startNpmOverlayE2E), and CI's shell smoke
// runs `go run ./internal/npmoverlay/cmd/npm-overlay`.
//
//forge:exclude-contract: test plumbing: one concrete *Overlay over a loopback HTTP server and the npm binary; tests drive it against a fake upstream, so there is nothing to substitute
package npmoverlay

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // npm's legacy dist.shasum is SHA-1 by definition; integrity is SHA-512
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// DefaultUpstream is the public npm registry.
const DefaultUpstream = "https://registry.npmjs.org"

// Package is one local package to overlay.
type Package struct {
	// Dir is the package's checkout (the directory holding its
	// package.json). It is packed on the first request for it, so a run
	// that never installs the package never pays for the pack.
	Dir string
	// Prepare, when set, runs once before the pack and owns building the
	// package; the pack then skips the package's lifecycle scripts (npm pack
	// --ignore-scripts) so the build is not run twice. When nil, npm pack
	// runs them (prepack/prepare), as npm publish does.
	Prepare func() error
}

// Options configures Start.
type Options struct {
	// Upstream is the registry everything else comes from. Default
	// DefaultUpstream.
	Upstream string
	// Packages are the local packages to overlay.
	Packages []Package
	// Log receives one line per pack and per failed request. Default
	// os.Stderr.
	Log io.Writer
}

// Overlay is a running overlay registry.
type Overlay struct {
	// URL is the registry's base URL, with a trailing slash.
	URL string

	upstream   string
	dir        string
	npmrc      string
	pnpmrc     string
	log        io.Writer
	client     *http.Client
	srv        *http.Server
	packages   map[string]*localPackage // by package name
	scopes     []string
	mu         sync.Mutex
	packuments map[string][]byte // upstream packuments, by package name
}

type localPackage struct {
	name, version string
	pkg           Package

	once      sync.Once
	err       error
	file      string // tarball base name, served under /-/tarballs/
	tarball   []byte
	manifest  map[string]any
	integrity string
	shasum    string
}

// Start listens on a loopback port and serves the overlay until Close.
func Start(opts Options) (*Overlay, error) {
	if len(opts.Packages) == 0 {
		return nil, errors.New("npmoverlay: no packages to overlay")
	}
	o := &Overlay{
		upstream:   strings.TrimRight(opts.Upstream, "/"),
		log:        opts.Log,
		client:     &http.Client{Timeout: 2 * time.Minute},
		packages:   map[string]*localPackage{},
		packuments: map[string][]byte{},
	}
	if o.upstream == "" {
		o.upstream = DefaultUpstream
	}
	if o.log == nil {
		o.log = os.Stderr
	}
	scopes := map[string]bool{}
	for _, p := range opts.Packages {
		name, version, err := readNameVersion(filepath.Join(p.Dir, "package.json"))
		if err != nil {
			return nil, err
		}
		scope, _, ok := strings.Cut(name, "/")
		if !ok || !strings.HasPrefix(scope, "@") {
			// Routing is per scope: an unscoped package would need the
			// whole registry pointed here.
			return nil, fmt.Errorf("npmoverlay: %s (%s) is not a scoped package", name, p.Dir)
		}
		if _, dup := o.packages[name]; dup {
			return nil, fmt.Errorf("npmoverlay: %s is overlaid twice", name)
		}
		o.packages[name] = &localPackage{name: name, version: version, pkg: p}
		if !scopes[scope] {
			scopes[scope] = true
			o.scopes = append(o.scopes, scope)
		}
	}
	// One temporary directory holds the npm config file and the packed
	// tarballs; Close removes it.
	d, err := os.MkdirTemp("", "npmoverlay-")
	if err != nil {
		return nil, err
	}
	o.dir = d

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		o.removeDir()
		return nil, err
	}
	o.URL = "http://" + ln.Addr().String() + "/"
	if err := o.writeNpmrc(); err != nil {
		_ = ln.Close()
		o.removeDir()
		return nil, err
	}
	o.srv = &http.Server{Handler: http.HandlerFunc(o.serve), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = o.srv.Serve(ln) }()
	return o, nil
}

// Env is the environment that routes npm to the overlay: KEY=VALUE pairs for
// exec.Cmd.Env, os.Setenv or a CI env file.
func (o *Overlay) Env() []string {
	names := make([]string, 0, len(o.packages))
	for name := range o.packages {
		names = append(names, name)
	}
	sort.Strings(names)
	exclude, _ := json.Marshal(names)
	return []string{
		"npm_config_globalconfig=" + o.npmrc,
		"pnpm_config_userconfig=" + o.pnpmrc,
		"pnpm_config_minimum_release_age_exclude=" + string(exclude),
	}
}

// Close stops the server and removes its temporary directory.
func (o *Overlay) Close() error {
	err := o.srv.Close()
	o.removeDir()
	return err
}

// Pack packs every overlaid package now, rather than on first request, and
// returns the first error. A caller that wants a failed build reported
// before any install starts calls it.
func (o *Overlay) Pack() error {
	for _, lp := range o.packages {
		if err := o.ensurePacked(lp); err != nil {
			return err
		}
	}
	return nil
}

func (o *Overlay) removeDir() {
	_ = os.RemoveAll(o.dir)
}

// writeNpmrc writes the two config files Env names: npm's global config and
// pnpm's user config. Each is the file its tool would otherwise read with the
// scope routes appended, so pointing the tool at it takes nothing else away.
func (o *Overlay) writeNpmrc() error {
	o.npmrc = filepath.Join(o.dir, "npmrc")
	if err := o.writeRouted(o.npmrc, existingGlobalConfig()); err != nil {
		return err
	}
	o.pnpmrc = filepath.Join(o.dir, "pnpmrc")
	return o.writeRouted(o.pnpmrc, existingUserConfig())
}

// writeRouted writes path as prev's contents (if any) plus the scope routes.
func (o *Overlay) writeRouted(path, prev string) error {
	var b strings.Builder
	if prev != "" {
		if data, err := os.ReadFile(prev); err == nil {
			b.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				b.WriteByte('\n')
			}
		}
	}
	for _, scope := range o.scopes {
		fmt.Fprintf(&b, "%s:registry=%s\n", scope, o.URL)
	}
	// The user's config can contain registry credentials. Keep its copy
	// private even though the parent temp directory is already mode 0700.
	return atomicfile.WriteFile(path, []byte(b.String()), 0o600)
}

// existingUserConfig is the user .npmrc pnpm would read without the
// overlay: an explicit pnpm_config_userconfig / npm_config_userconfig, else
// ~/.npmrc. "" when there is no home directory to find it in.
func existingUserConfig() string {
	for _, k := range []string{"pnpm_config_userconfig", "PNPM_CONFIG_USERCONFIG", "npm_config_userconfig", "NPM_CONFIG_USERCONFIG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".npmrc")
}

// existingGlobalConfig is the global npmrc npm would read without the
// overlay: an explicit npm_config_globalconfig, else <prefix>/etc/npmrc, where
// the prefix is npm_config_prefix or, as npm derives it, the directory above
// node's bin/ (node's own directory on Windows). Derived rather than asked of
// `npm config get`, which costs a node start-up per overlay.
func existingGlobalConfig() string {
	for _, k := range []string{"npm_config_globalconfig", "NPM_CONFIG_GLOBALCONFIG"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	prefix := ""
	for _, k := range []string{"npm_config_prefix", "NPM_CONFIG_PREFIX"} {
		if v := os.Getenv(k); v != "" {
			prefix = v
			break
		}
	}
	if prefix == "" {
		node, err := exec.LookPath("node")
		if err != nil {
			return ""
		}
		if resolved, err := filepath.EvalSymlinks(node); err == nil {
			node = resolved
		}
		prefix = filepath.Dir(node)
		if runtime.GOOS != "windows" {
			prefix = filepath.Dir(prefix)
		}
	}
	return filepath.Join(prefix, "etc", "npmrc")
}

func (o *Overlay) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "the overlay registry is read-only", http.StatusMethodNotAllowed)
		return
	}
	// npm escapes a scoped name's slash (@scope%2fname); net/http has
	// already decoded it into Path.
	name := strings.TrimPrefix(r.URL.Path, "/")
	if rest, ok := strings.CutPrefix(name, "-/tarballs/"); ok {
		o.serveTarball(w, rest)
		return
	}
	lp := o.packages[name]
	if lp == nil {
		o.proxy(w, r, name)
		return
	}
	if err := o.ensurePacked(lp); err != nil {
		fmt.Fprintf(o.log, "npmoverlay: %s: %v\n", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	doc, err := o.packument(lp)
	if err != nil {
		fmt.Fprintf(o.log, "npmoverlay: %s: %v\n", name, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}

// serveTarball serves <name>/<file>, the URL mergePackument hands out.
func (o *Overlay) serveTarball(w http.ResponseWriter, rest string) {
	i := strings.LastIndex(rest, "/")
	if i < 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	lp := o.packages[rest[:i]]
	// ensurePacked, not a bare read of lp.file: the once is what orders this
	// goroutine after the one that packed.
	if lp == nil || o.ensurePacked(lp) != nil || lp.file != rest[i+1:] {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(lp.tarball)
}

// proxy relays a request for a package the overlay does not replace.
func (o *Overlay) proxy(w http.ResponseWriter, r *http.Request, name string) {
	body, status, err := o.fetchUpstream(name, r.Header.Get("Accept"))
	if err != nil {
		fmt.Fprintf(o.log, "npmoverlay: proxy %s: %v\n", name, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (o *Overlay) fetchUpstream(name, accept string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, o.upstream+"/"+escapeName(name), nil)
	if err != nil {
		return nil, 0, err
	}
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// packument is the upstream document for lp with the local version in it.
func (o *Overlay) packument(lp *localPackage) ([]byte, error) {
	o.mu.Lock()
	cached, ok := o.packuments[lp.name]
	o.mu.Unlock()
	if !ok {
		// The FULL document, not the abbreviated install form: the local
		// manifest is a full one, and npm reads either.
		body, status, err := o.fetchUpstream(lp.name, "application/json")
		switch {
		case err != nil:
			return nil, fmt.Errorf("fetch the upstream packument: %w", err)
		case status == http.StatusNotFound:
			body = nil // never published: the local version is the only one
		case status != http.StatusOK:
			return nil, fmt.Errorf("fetch the upstream packument: HTTP %d: %s", status, strings.TrimSpace(string(body)))
		}
		cached = body
		o.mu.Lock()
		o.packuments[lp.name] = cached
		o.mu.Unlock()
	}
	return mergePackument(cached, lp, o.URL)
}

// mergePackument adds lp's version to an upstream packument (nil: none).
func mergePackument(upstream []byte, lp *localPackage, base string) ([]byte, error) {
	doc := map[string]any{}
	if len(upstream) > 0 {
		if err := json.Unmarshal(upstream, &doc); err != nil {
			return nil, fmt.Errorf("parse the upstream packument: %w", err)
		}
	}
	doc["name"] = lp.name
	versions, _ := doc["versions"].(map[string]any)
	if versions == nil {
		versions = map[string]any{}
	}
	m := make(map[string]any, len(lp.manifest)+2)
	for k, v := range lp.manifest {
		m[k] = v
	}
	m["_id"] = lp.name + "@" + lp.version
	m["dist"] = map[string]any{
		"tarball":   base + "-/tarballs/" + escapeName(lp.name) + "/" + lp.file,
		"integrity": lp.integrity,
		"shasum":    lp.shasum,
	}
	versions[lp.version] = m
	doc["versions"] = versions

	tags, _ := doc["dist-tags"].(map[string]any)
	if tags == nil {
		tags = map[string]any{}
	}
	// npm prefers `latest` whenever it satisfies the range, so a local
	// version ahead of the release must become latest, or a `^0.11.0`
	// range would install the published 0.11.0 over a local 0.11.1.
	if latest, _ := tags["latest"].(string); latest == "" || semver.Compare("v"+lp.version, "v"+latest) >= 0 {
		tags["latest"] = lp.version
	}
	doc["dist-tags"] = tags
	// A stale `time` map is harmless, but `modified` is what npm's cache
	// revalidation reads; say the document changed now.
	if t, ok := doc["time"].(map[string]any); ok {
		now := time.Now().UTC().Format(time.RFC3339)
		t[lp.version] = now
		t["modified"] = now
	}
	return json.Marshal(doc)
}

func (o *Overlay) ensurePacked(lp *localPackage) error {
	lp.once.Do(func() { lp.err = o.pack(lp) })
	return lp.err
}

func (o *Overlay) pack(lp *localPackage) error {
	start := time.Now()
	if lp.pkg.Prepare != nil {
		if err := lp.pkg.Prepare(); err != nil {
			return fmt.Errorf("prepare %s: %w", lp.pkg.Dir, err)
		}
	}
	dest, err := os.MkdirTemp(o.dir, "pack-")
	if err != nil {
		return err
	}
	args := []string{"pack", "--pack-destination", dest}
	if lp.pkg.Prepare != nil {
		args = append(args, "--ignore-scripts")
	}
	cmd := exec.Command("npm", args...)
	cmd.Dir = lp.pkg.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm %s in %s: %w\n%s", strings.Join(args, " "), lp.pkg.Dir, err, out)
	}
	// A fresh directory holds exactly the one tarball npm wrote; reading
	// the name from the directory, not stdout, keeps a lifecycle script's
	// output from being mistaken for it.
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".tgz") {
		return fmt.Errorf("npm pack in %s left %d files in %s, want one .tgz", lp.pkg.Dir, len(entries), dest)
	}
	tgz := filepath.Join(dest, entries[0].Name())
	if err := lp.load(tgz); err != nil {
		return err
	}
	fmt.Fprintf(o.log, "npmoverlay: packed %s@%s from %s (%d bytes, %s)\n",
		lp.name, lp.version, lp.pkg.Dir, len(lp.tarball), time.Since(start).Round(time.Millisecond))
	return nil
}

// load reads a packed tarball: its bytes, digests and package.json.
func (lp *localPackage) load(tgz string) error {
	data, err := os.ReadFile(tgz)
	if err != nil {
		return err
	}
	manifest, err := tarballManifest(data)
	if err != nil {
		return fmt.Errorf("%s: %w", tgz, err)
	}
	if manifest["name"] != lp.name || manifest["version"] != lp.version {
		return fmt.Errorf("%s holds %v@%v, want %s@%s (did package.json change after the overlay started?)",
			tgz, manifest["name"], manifest["version"], lp.name, lp.version)
	}
	sum512 := sha512.Sum512(data)
	sum1 := sha1.Sum(data) //nolint:gosec // see the import
	lp.tarball = data
	lp.manifest = manifest
	lp.file = path.Base(filepath.ToSlash(tgz))
	lp.integrity = "sha512-" + base64.StdEncoding.EncodeToString(sum512[:])
	lp.shasum = hex.EncodeToString(sum1[:])
	return nil
}

// tarballManifest returns package/package.json from an npm tarball.
func tarballManifest(data []byte) (map[string]any, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no package/package.json in the tarball")
		}
		if err != nil {
			return nil, err
		}
		if h.Name != "package/package.json" {
			continue
		}
		var m map[string]any
		if err := json.NewDecoder(tr).Decode(&m); err != nil {
			return nil, fmt.Errorf("package/package.json: %w", err)
		}
		return m, nil
	}
}

func readNameVersion(manifestPath string) (string, string, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", fmt.Errorf("npmoverlay: %w", err)
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", fmt.Errorf("npmoverlay: %s: %w", manifestPath, err)
	}
	if m.Name == "" || m.Version == "" {
		return "", "", fmt.Errorf("npmoverlay: %s names no package and version", manifestPath)
	}
	return m.Name, m.Version, nil
}

// escapeName is a package name as a registry path: a scoped name's slash
// escaped, as npm sends it.
func escapeName(name string) string {
	if scope, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return scope + "%2f" + url.PathEscape(rest)
	}
	return url.PathEscape(name)
}
