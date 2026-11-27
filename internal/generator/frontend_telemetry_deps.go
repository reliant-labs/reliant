package generator

// The browser-telemetry SDK a frontend that imports
// @reliantlabs/forge-web-runtime/telemetry must declare itself.
//
// web-runtime 0.13.19 through 0.13.x carried @hyperdx/otel-web and rrweb as
// its own DEPENDENCIES. npm installs a dependency into every consumer whether
// or not an entry it imports reaches it, so a consumer of './node' alone (an
// Electron main process) installed about 90 MB of browser packages for
// nothing (forge defect ae15c269). From 0.14.0 both are exact OPTIONAL peers:
// only './telemetry' imports them, and the app that imports it supplies them.
//
// So forge declares them in every frontend whose sources import
// './telemetry' AND whose runtime is one that takes them as peers: each web
// scaffold renders them into package.json, and `forge generate` adds them to
// an existing frontend (scaffolded or adopted) on the run that raises its
// runtime floor to 0.14, before any install can resolve a runtime without
// them. A frontend whose manifest holds its runtime below 0.14 (a disowned
// manifest, a hand-pinned range) is left alone: that runtime brings its own
// copies, and a project's chosen version there is not forge's to rewrite.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// webRuntimeTelemetryEntry is the runtime subpath that imports the SDK.
const webRuntimeTelemetryEntry = WebRuntimePackage + "/telemetry"

// telemetryPeers are the runtime peers './telemetry' imports, which a
// frontend importing it must declare. Their specifiers come from the runtime's
// own peerDependencies (webruntimepeers), so the version lives in one place.
var telemetryPeers = []string{"@hyperdx/otel-web", "rrweb"}

// exactVersionRe matches a bare x.y.z, the shape of an exact peer specifier.
var exactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// telemetryPeerSpecs returns name → the specifier the runtime declares, for
// every telemetry peer the runtime declares. A peer it stops declaring is
// simply not managed any more.
func telemetryPeerSpecs() map[string]string {
	out := map[string]string{}
	for _, name := range telemetryPeers {
		if spec, ok := webruntimepeers.PeerSpec(name); ok {
			out[name] = spec
		}
	}
	return out
}

// wantTelemetryPeer returns the specifier forge wants for a telemetry peer the
// manifest declares as current. An EXACT peer admits one version, and npm
// refuses an install (ERESOLVE) that puts any other beside the runtime, so a
// different value is replaced. A range peer is a floor, like the runtime's
// own (specSatisfiesFloor): a value at or above it is the project's to keep.
func wantTelemetryPeer(current, spec string) string {
	if current == spec {
		return current
	}
	if !exactVersionRe.MatchString(spec) && specSatisfiesFloor(current, spec) {
		return current
	}
	return spec
}

// frontendImportsTelemetry reports whether any source file of the frontend at
// feDir imports '@reliantlabs/forge-web-runtime/telemetry'. Every forge web
// scaffold does (src/lib/otel_gen.ts); an adopted app may, from any file.
// Build output, dependencies and dot-directories are not sources.
func frontendImportsTelemetry(feDir string) bool {
	needle := []byte(webRuntimeTelemetryEntry)
	found := false
	_ = filepath.WalkDir(feDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: not a source this can judge
		}
		if d.IsDir() {
			if path == feDir {
				return nil
			}
			switch name := d.Name(); {
			case strings.HasPrefix(name, "."), name == "node_modules", name == "dist", name == "out",
				name == "build", name == "coverage":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, needle) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// The two top-level install scopes a manifest declares packages in.
const (
	sectionDependencies    = "dependencies"
	sectionDevDependencies = "devDependencies"
)

// manifestScopes is a manifest's two top-level install scopes.
type manifestScopes struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (m manifestScopes) section(name string) map[string]string {
	if name == sectionDevDependencies {
		return m.DevDependencies
	}
	return m.Dependencies
}

// telemetryPeersSince is the first runtime release that takes the telemetry
// SDK as optional peers instead of carrying it as dependencies.
const telemetryPeersSince = "v0.14.0"

// telemetryPeerTarget is what a frontend's manifest must declare for the
// runtime it resolves: each telemetry peer at the specifier THAT runtime
// declares, in the install scope the runtime itself is declared in, so the
// peers install exactly when the runtime does.
type telemetryPeerTarget struct {
	section string            // sectionDependencies or sectionDevDependencies
	specs   map[string]string // peer name → the runtime's specifier for it
}

// telemetryPeerTargetFor decides the target for the manifest text at feDir,
// and false when the runtime it declares does not take the telemetry SDK as
// peers, so the app need not supply it:
//
//   - a version range whose floor is 0.14.0 or later: every release it
//     admits declares the peers, at the versions the checked-in runtime
//     declares (webruntimepeers);
//   - a `file:` bridge (a dev forge's local runtime): exactly the telemetry
//     peers that runtime's own package.json declares, at its versions, so the
//     manifest matches the runtime it links rather than the checked-in one.
//
// Anything else is no: a floor below 0.14 (the older runtime carries the
// packages itself), no runtime entry, or a specifier this cannot read (a git
// URL, an npm alias). The manifest is then left as the project wrote it.
func telemetryPeerTargetFor(feDir, text string) (telemetryPeerTarget, bool) {
	var doc manifestScopes
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return telemetryPeerTarget{}, false
	}
	section := sectionDependencies
	spec, ok := doc.Dependencies[WebRuntimePackage]
	if !ok {
		if spec, ok = doc.DevDependencies[WebRuntimePackage]; !ok {
			return telemetryPeerTarget{}, false
		}
		section = sectionDevDependencies
	}
	if path, ok := strings.CutPrefix(spec, "file:"); ok {
		if !filepath.IsAbs(path) {
			path = filepath.Join(feDir, path)
		}
		b, err := os.ReadFile(filepath.Join(path, "package.json"))
		if err != nil {
			return telemetryPeerTarget{}, false
		}
		var runtime struct {
			PeerDependencies map[string]string `json:"peerDependencies"`
		}
		if json.Unmarshal(b, &runtime) != nil {
			return telemetryPeerTarget{}, false
		}
		specs := map[string]string{}
		for _, name := range telemetryPeers {
			if s, ok := runtime.PeerDependencies[name]; ok && s != "" {
				specs[name] = s
			}
		}
		return telemetryPeerTarget{section: section, specs: specs}, len(specs) > 0
	}
	floor, ok := minimumVersion(spec)
	if !ok || semver.Compare(floor, telemetryPeersSince) < 0 {
		return telemetryPeerTarget{}, false
	}
	specs := telemetryPeerSpecs()
	return telemetryPeerTarget{section: section, specs: specs}, len(specs) > 0
}

// declaredPeer is where a manifest already declares a peer, if anywhere: the
// runtime's own scope first, then the other one, so a peer the project put in
// the other scope is reconciled where it is rather than declared twice.
func declaredPeer(doc manifestScopes, target telemetryPeerTarget, name string) (section, current string, found bool) {
	other := sectionDevDependencies
	if target.section == sectionDevDependencies {
		other = sectionDependencies
	}
	for _, s := range []string{target.section, other} {
		if v, ok := doc.section(s)[name]; ok {
			return s, v, true
		}
	}
	return "", "", false
}

// sectionOpenRe matches the opening brace of the top-level object key.
func sectionOpenRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*\{`)
}

// replaceInSection replaces the value of name's entry inside the object key
// with spec, and false when that entry is not where this splice can reach.
// A manifest's dependency objects are flat string maps, so the object ends
// at its first closing brace.
func replaceInSection(text, key, name, spec string) (string, bool) {
	open := sectionOpenRe(key).FindStringIndex(text)
	if open == nil {
		return text, false
	}
	end := strings.IndexByte(text[open[1]:], '}')
	if end < 0 {
		return text, false
	}
	body := text[open[1] : open[1]+end]
	at := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*:\s*("[^"]*")`).FindStringSubmatchIndex(body)
	if at == nil {
		return text, false
	}
	base := open[1]
	return text[:base+at[2]] + jsonString(spec) + text[base+at[3]:], true
}

// EnsureTelemetryDependencies declares the telemetry peers in
// <feRelDir>/package.json when the frontend's sources import
// '@reliantlabs/forge-web-runtime/telemetry', at the specifier the runtime
// declares for each. Surgical, like EnsureWebRuntimeDependency: one value
// replaced or one entry inserted, a run that changes nothing writes nothing,
// and every failure is a warning. It reports whether it rewrote the manifest.
//
// A frontend that never imports './telemetry' (Expo, an adopted app without
// telemetry) is left alone: the point of the peers being optional is that it
// installs none of them. So is one whose runtime is older than 0.14
// (runtimeTakesTelemetryPeers): that runtime still carries the packages.
func EnsureTelemetryDependencies(projectDir, feRelDir string) bool {
	feDir := filepath.Join(projectDir, feRelDir)
	pkgPath := filepath.Join(feDir, "package.json")
	info, err := os.Stat(pkgPath)
	if err != nil || !frontendImportsTelemetry(feDir) {
		return false
	}
	original, err := os.ReadFile(pkgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", pkgPath, err)
		return false
	}
	text := string(original)
	target, ok := telemetryPeerTargetFor(feDir, text)
	if !ok {
		return false
	}
	var doc manifestScopes
	if err := json.Unmarshal(original, &doc); err != nil {
		return false
	}
	for _, name := range sortedKeys(target.specs) {
		spec := target.specs[name]
		section, current, found := declaredPeer(doc, target, name)
		switch {
		case found && wantTelemetryPeer(current, spec) == current:
			continue
		case found:
			replaced, spliced := replaceInSection(text, section, name, spec)
			if !spliced {
				continue // declared somewhere this splice cannot reach; leave it
			}
			text = replaced
		default:
			text, err = insertIntoObject(text, sectionOpenRe(target.section), target.section, name, spec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not declare %s in %s: %v\n", name, pkgPath, err)
				return false
			}
		}
	}
	if text == string(original) {
		return false
	}
	if !json.Valid([]byte(text)) {
		fmt.Fprintf(os.Stderr, "warning: declaring the telemetry SDK in %s would have produced invalid JSON; left unchanged\n", pkgPath)
		return false
	}
	checksums.RecordPreWriteAbs(pkgPath)
	if err := atomicfile.WriteFile(pkgPath, []byte(text), info.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", pkgPath, err)
		return false
	}
	return true
}

// telemetryDependencyNotices is EnsureTelemetryDependencies' read-only
// counterpart for a disowned manifest: one line per peer it would declare or
// change.
func telemetryDependencyNotices(feDir, text string) []string {
	if !frontendImportsTelemetry(feDir) {
		return nil
	}
	target, ok := telemetryPeerTargetFor(feDir, text)
	if !ok {
		return nil
	}
	var doc manifestScopes
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil
	}
	install := "npm install"
	if target.section == sectionDevDependencies {
		install = "npm install --save-dev"
	}
	var out []string
	for _, name := range sortedKeys(target.specs) {
		spec := target.specs[name]
		_, current, found := declaredPeer(doc, target, name)
		switch {
		case !found:
			out = append(out, fmt.Sprintf("this frontend imports %s, whose peer %s package.json does not declare — `%s %s@%s`",
				webRuntimeTelemetryEntry, name, install, name, spec))
		case wantTelemetryPeer(current, spec) != current:
			out = append(out, fmt.Sprintf("package.json declares %s %s; %s requires exactly %s — `npm install %s@%s`",
				name, current, WebRuntimePackage, spec, name, spec))
		}
	}
	return out
}
