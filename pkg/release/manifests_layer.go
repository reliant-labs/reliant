package release

// ValidateManifestsLayer: the INGEST GATE for a bundle's manifests layer.
//
// The layer is what a Flux shared by many organizations builds and applies,
// so its bytes are the one input a hostile organization fully controls. kustomize-controller
// honours a kustomization file it finds in the artifact (generators, patches,
// components, file loads), and otherwise generates its own kustomization over
// every *.yaml it finds — so the safe input is plain, validated YAML and
// nothing that reaches kustomize's feature surface. This package decides what
// "plain, validated" means, once, and BOTH sides call it: forge at build time
// (fail fast, before anything is pushed) and the control plane at ingest (the
// gate that does not trust the producer). The producer and the gate cannot
// disagree because there is one function.
//
// THE LAYOUT GRAMMAR. Every entry is a regular file or a directory, at one of:
//
//	manifests/<cluster>/<file>.yaml                 today's layout (v1)
//	manifests/<cluster>/<unit>/<phase>/<file>.yaml  cluster × unit × phase trees (v2)
//	observability/dashboards/<name>.json            data, never applied
//	observability/metrics-schema.json               data, never applied
//
// <cluster> is [BundleClusterPath]'s segment (`_` is the unclustered tree);
// <unit> is a delivery unit; <phase> is one of pre, rollout or post. All three
// are single segments over [A-Za-z0-9._-]. A cluster directory holds v1 files
// OR v2 unit directories, never both. Each manifest file holds exactly one
// document.
//
// THIS IS A DENY-BY-DEFAULT GRAMMAR, not a blocklist: a path the grammar does
// not name is refused. The explicit refusals (kustomization files, symlinks,
// traversal) exist so the error says what is wrong rather than "not in the
// grammar", and so a future widening of the grammar cannot quietly re-admit
// them.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Bounds on a manifests layer. They sit far above a real bundle (control-plane
// prod is 2.3 MiB uncompressed) and far below anything that threatens the
// controller's memory, so they exist to make hostile input fail loud and early.
const (
	// ManifestsLayerMaxFileBytes bounds any single file.
	ManifestsLayerMaxFileBytes = 1 << 20
	// ManifestsLayerMaxBytes bounds the sum of every file's bytes,
	// uncompressed.
	ManifestsLayerMaxBytes = 32 << 20
	// ManifestsLayerMaxFiles bounds the regular files, which is also the
	// document count: one document per manifest file.
	ManifestsLayerMaxFiles = 5000

	manifestsLayerMaxEntries = 2 * ManifestsLayerMaxFiles
	// manifestsLayerMaxStream bounds the DECOMPRESSED tar stream — headers
	// and padding included — counted on the read side, so a gzip bomb cannot
	// hide behind a small declared size.
	manifestsLayerMaxStream = ManifestsLayerMaxBytes + 16<<20

	manifestsLayerMaxYAMLDepth = 100
	manifestsLayerMaxPath      = 1024
	manifestsLayerMaxSegment   = 400
	manifestsLayerMaxViolation = 25
	manifestsLayerMaxNameBytes = 253
)

// ErrInvalidManifestsLayer is wrapped by every refusal from
// [ValidateManifestsLayer], so a caller can tell "this bundle is hostile or
// malformed" from an IO failure reading it.
var ErrInvalidManifestsLayer = errors.New("invalid manifests layer")

// LayerViolation is one refusal, naming the offending file.
type LayerViolation struct {
	// File is the archive entry's path as written, or "(archive)" for a
	// problem with the stream itself (not gzip, not tar, too large).
	File   string
	Reason string
}

func (v LayerViolation) String() string { return v.File + ": " + v.Reason }

// ManifestsLayerError is what [ValidateManifestsLayer] returns for an invalid
// layer: every violation found, up to a cap, each naming its file.
type ManifestsLayerError struct {
	Violations []LayerViolation
	// Truncated is true when more violations existed than are listed.
	Truncated bool
}

func (e *ManifestsLayerError) Error() string {
	var b strings.Builder
	b.WriteString(ErrInvalidManifestsLayer.Error())
	if len(e.Violations) == 1 && !e.Truncated {
		b.WriteString(": ")
		b.WriteString(e.Violations[0].String())
		return b.String()
	}
	fmt.Fprintf(&b, " (%d violations", len(e.Violations))
	if e.Truncated {
		b.WriteString(", more not listed")
	}
	b.WriteString("):")
	for _, v := range e.Violations {
		b.WriteString("\n  ")
		b.WriteString(v.String())
	}
	return b.String()
}

// Is makes errors.Is(err, ErrInvalidManifestsLayer) hold.
func (e *ManifestsLayerError) Is(target error) bool { return target == ErrInvalidManifestsLayer }

// LayerObject is one validated manifest: where it sits and what it declares.
// The control plane's per-env kind check reads these rather than re-parsing.
type LayerObject struct {
	Path       string
	Cluster    string
	Unit       string // "" in the v1 layout
	Phase      string // "" in the v1 layout
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// ManifestsLayerSummary describes a layer that passed.
type ManifestsLayerSummary struct {
	// Files counts every regular file: manifests plus data artifacts.
	Files int
	// Bytes is the sum of the files' bytes, uncompressed.
	Bytes int64
	// Objects is every manifest, sorted by Path.
	Objects []LayerObject
	// Artifacts is every non-manifest data file (observability JSON),
	// sorted. Nothing applies them.
	Artifacts []string
}

// layerPhases are the phase directories of the v2 layout, in apply order.
var layerPhases = map[string]bool{"pre": true, "rollout": true, "post": true}

// ValidateManifestsLayer validates a bundle's manifests layer: a gzipped tar
// read from r (the blob of media type [BundleManifestsLayer]).
//
// It enforces, over the bytes actually read rather than anything the archive
// declares:
//   - the layout grammar in this file's header;
//   - entry types: regular files and directories only — no symlink, hardlink,
//     device, fifo or global header;
//   - no `..` or `.` segment, no absolute path, no backslash, no duplicate;
//   - no kustomization file at any depth (`kustomization.yaml`, `.yml`,
//     `Kustomization`, any case), and no manifest of apiVersion
//     `kustomize.config.k8s.io/*`;
//   - `.yaml` for manifests, and only the observability JSON for data;
//   - per-file, total, file-count, path-length and decompressed-stream bounds;
//   - strict YAML: exactly one document per file, no anchors, aliases, merge
//     keys or non-core tags, no duplicate keys, a nesting limit;
//   - every manifest carries a string apiVersion, kind and metadata.name.
//
// A nil error means the layer is safe to hand to Flux. Otherwise the error is
// a *[ManifestsLayerError] naming every offending file it found (up to a cap).
// Kind scope — which kinds an env may carry — is deliberately NOT decided
// here: it depends on the env, and the summary's Objects hand it to the caller.
func ValidateManifestsLayer(r io.Reader) (ManifestsLayerSummary, error) {
	return validateManifestsLayer(r, defaultLayerLimits)
}

// layerLimits are the bounds a validation enforces. Production always uses
// [defaultLayerLimits]; tests shrink them so the bound tests do not have to
// build tens of megabytes.
type layerLimits struct {
	fileBytes, totalBytes, streamBytes int64
	files, entries                     int
}

var defaultLayerLimits = layerLimits{
	fileBytes:   ManifestsLayerMaxFileBytes,
	totalBytes:  ManifestsLayerMaxBytes,
	streamBytes: manifestsLayerMaxStream,
	files:       ManifestsLayerMaxFiles,
	entries:     manifestsLayerMaxEntries,
}

func validateManifestsLayer(r io.Reader, limits layerLimits) (ManifestsLayerSummary, error) {
	v := &layerValidator{
		limits:   limits,
		seen:     map[string]bool{},
		clusters: map[string]*clusterLayout{},
	}
	v.run(r)
	if len(v.violations) > 0 {
		return ManifestsLayerSummary{}, &ManifestsLayerError{Violations: v.violations, Truncated: v.truncated}
	}
	sort.Slice(v.summary.Objects, func(i, j int) bool { return v.summary.Objects[i].Path < v.summary.Objects[j].Path })
	sort.Strings(v.summary.Artifacts)
	return v.summary, nil
}

type clusterLayout struct {
	files bool // has v1 files directly under it
	units bool // has v2 unit directories
}

type layerValidator struct {
	limits     layerLimits
	seen       map[string]bool
	clusters   map[string]*clusterLayout
	summary    ManifestsLayerSummary
	violations []LayerViolation
	truncated  bool
	entries    int
	// stop is set by a refusal after which reading further would only spend
	// the validator's time on a layer that is already refused.
	stop bool
}

func (v *layerValidator) fail(file, format string, args ...any) {
	if len(v.violations) >= manifestsLayerMaxViolation {
		v.truncated = true
		return
	}
	v.violations = append(v.violations, LayerViolation{File: file, Reason: fmt.Sprintf(format, args...)})
}

// fatal records a stream-level problem. Nothing after it can be trusted.
func (v *layerValidator) fatal(format string, args ...any) { v.fail("(archive)", format, args...) }

func (v *layerValidator) run(r io.Reader) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		v.fatal("not a gzip stream: %v", err)
		return
	}
	defer func() { _ = gz.Close() }()
	limited := &io.LimitedReader{R: gz, N: v.limits.streamBytes + 1}
	tr := tar.NewReader(limited)
	exhausted := func() bool { return limited.N <= 0 }

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if exhausted() {
				v.fatal("decompressed stream exceeds %d bytes", v.limits.streamBytes)
			} else {
				v.fatal("malformed tar: %v", err)
			}
			return
		}
		v.entries++
		if v.entries > v.limits.entries {
			v.fatal("more than %d entries", v.limits.entries)
			return
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			v.dir(hdr)
		case tar.TypeReg:
			if !v.file(hdr, tr) && exhausted() {
				v.fatal("decompressed stream exceeds %d bytes", v.limits.streamBytes)
				return
			}
			if v.stop {
				return
			}
		default:
			v.fail(hdr.Name, "entry type %s is not allowed (regular files and directories only; symlinks, hardlinks and devices are refused)", typeName(hdr.Typeflag))
		}
	}
	if exhausted() {
		v.fatal("decompressed stream exceeds %d bytes", v.limits.streamBytes)
	}
}

func typeName(t byte) string {
	switch t {
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hardlink"
	case tar.TypeChar:
		return "character device"
	case tar.TypeBlock:
		return "block device"
	case tar.TypeFifo:
		return "fifo"
	case tar.TypeXGlobalHeader:
		return "pax global header"
	default:
		return fmt.Sprintf("%q", string(t))
	}
}

// cleanPath refuses every spelling of an escape and returns the entry's
// segments. The name is judged as written: nothing is cleaned into acceptance,
// because a scrub is a blocklist and the archive must already be canonical.
func cleanPath(name string) ([]string, string) {
	switch {
	case name == "":
		return nil, "empty path"
	case len(name) > manifestsLayerMaxPath:
		return nil, fmt.Sprintf("path is %d bytes, over the %d-byte bound", len(name), manifestsLayerMaxPath)
	case strings.HasPrefix(name, "/"):
		return nil, "absolute path"
	case strings.ContainsAny(name, "\\\x00"):
		return nil, "path contains a backslash or NUL"
	}
	trimmed := strings.TrimSuffix(name, "/")
	segs := strings.Split(trimmed, "/")
	for _, s := range segs {
		switch {
		case s == "..":
			return nil, "path contains a `..` segment"
		case s == "." || s == "":
			return nil, "path contains an empty or `.` segment"
		case len(s) > manifestsLayerMaxSegment:
			return nil, fmt.Sprintf("path segment is %d bytes, over the %d-byte bound", len(s), manifestsLayerMaxSegment)
		case !segmentOK(s):
			return nil, fmt.Sprintf("path segment %q has characters outside [A-Za-z0-9._-]", s)
		}
	}
	return segs, ""
}

func segmentOK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// isKustomizationName is true for every FILE name kustomize loads as a
// kustomization: `kustomization.yaml`, `kustomization.yml`, `Kustomization`.
// Case-insensitive and extension-agnostic on purpose — a near miss that a
// case-folding filesystem or a future kustomize would honour is still a
// kustomization. Directories are not checked: a directory is only special to
// kustomize through a kustomization file inside it, which is refused itself.
func isKustomizationName(base string) bool {
	l := strings.ToLower(base)
	for _, ext := range []string{".yaml", ".yml", ".json"} {
		l = strings.TrimSuffix(l, ext)
	}
	return l == "kustomization"
}

// place is where a manifest file sits in the grammar.
type place struct {
	cluster, unit, phase string
	artifact             bool // an observability data file
}

func (v *layerValidator) dir(hdr *tar.Header) {
	segs, why := cleanPath(hdr.Name)
	if why != "" {
		v.fail(hdr.Name, "%s", why)
		return
	}
	if !dirInGrammar(segs) {
		v.fail(hdr.Name, "directory is not in the bundle layout (manifests/<cluster>[/<unit>/<phase>] or observability[/dashboards])")
		return
	}
	key := strings.Join(segs, "/")
	if v.seen[key] {
		v.fail(hdr.Name, "appears more than once")
		return
	}
	v.seen[key] = true
	if len(segs) == 3 && segs[0] == "manifests" {
		v.markUnits(segs[1], hdr.Name)
	}
}

func dirInGrammar(segs []string) bool {
	switch segs[0] {
	case "manifests":
		switch len(segs) {
		case 1, 2, 3:
			return true
		case 4:
			return layerPhases[segs[3]]
		}
	case "forge":
		return len(segs) == 1
	case "observability":
		return len(segs) == 1 || (len(segs) == 2 && segs[1] == "dashboards")
	}
	return false
}

func (v *layerValidator) markUnits(cluster, file string) {
	c := v.clusterOf(cluster)
	c.units = true
	if c.files {
		v.fail(file, "cluster %q mixes files directly under it (v1 layout) with unit directories (v2 layout)", cluster)
	}
}

func (v *layerValidator) clusterOf(name string) *clusterLayout {
	c := v.clusters[name]
	if c == nil {
		c = &clusterLayout{}
		v.clusters[name] = c
	}
	return c
}

// file validates one regular file and reports whether its body was read to
// the end without a read error.
func (v *layerValidator) file(hdr *tar.Header, body io.Reader) bool {
	segs, why := cleanPath(hdr.Name)
	if why != "" {
		v.fail(hdr.Name, "%s", why)
		return true
	}
	if strings.HasSuffix(hdr.Name, "/") {
		v.fail(hdr.Name, "regular file with a directory name")
		return true
	}
	base := segs[len(segs)-1]
	if isKustomizationName(base) {
		v.fail(hdr.Name, "kustomization files are not allowed at any depth (the shared controller would honour its generators, patches and file loads)")
		return true
	}
	key := strings.Join(segs, "/")
	if v.seen[key] {
		v.fail(hdr.Name, "appears more than once")
		return true
	}
	v.seen[key] = true

	p, ok := v.classifyFile(hdr.Name, segs)
	if !ok {
		return true
	}

	v.summary.Files++
	if v.summary.Files > v.limits.files {
		v.fail(hdr.Name, "more than %d files", v.limits.files)
		v.stop = true
		return true
	}
	if hdr.Size > v.limits.fileBytes {
		v.fail(hdr.Name, "file is %d bytes, over the %d-byte bound", hdr.Size, v.limits.fileBytes)
		return true
	}
	// Read what is there, not what the header says: Size is attacker-supplied.
	data, err := io.ReadAll(io.LimitReader(body, v.limits.fileBytes+1))
	if err != nil {
		return false
	}
	if int64(len(data)) > v.limits.fileBytes {
		v.fail(hdr.Name, "file is over the %d-byte bound", v.limits.fileBytes)
		return true
	}
	v.summary.Bytes += int64(len(data))
	if v.summary.Bytes > v.limits.totalBytes {
		v.fail(hdr.Name, "layer exceeds %d bytes uncompressed", v.limits.totalBytes)
		v.stop = true
		return true
	}

	if p.artifact {
		v.artifact(hdr.Name, data)
		return true
	}
	v.manifest(hdr.Name, p, data)
	return true
}

// classifyFile places a file in the grammar, recording the v1/v2 choice of a
// cluster as a side effect.
func (v *layerValidator) classifyFile(name string, segs []string) (place, bool) {
	base := segs[len(segs)-1]
	switch segs[0] {
	case "forge":
		if name != BundleFrontendDeclarationsPath {
			v.fail(name, "only the dedicated frozen frontend declaration asset is permitted under forge/")
			return place{}, false
		}
		return place{artifact: true}, true
	case "observability":
		isDash := len(segs) == 3 && segs[1] == "dashboards" && strings.HasSuffix(base, ".json") && base != ".json"
		isSchema := len(segs) == 2 && base == "metrics-schema.json"
		if !isDash && !isSchema {
			v.fail(name, "not in the bundle layout: observability data is observability/dashboards/<name>.json or observability/metrics-schema.json")
			return place{}, false
		}
		return place{artifact: true}, true
	case "manifests":
		if !strings.HasSuffix(base, ".yaml") || base == ".yaml" {
			if strings.HasSuffix(strings.ToLower(base), ".yml") {
				v.fail(name, "manifests must be .yaml (.yml is refused)")
			} else {
				v.fail(name, "not YAML: manifest files must be <name>.yaml")
			}
			return place{}, false
		}
		switch len(segs) {
		case 3: // manifests/<cluster>/<file>.yaml
			c := v.clusterOf(segs[1])
			c.files = true
			if c.units {
				v.fail(name, "cluster %q mixes files directly under it (v1 layout) with unit directories (v2 layout)", segs[1])
				return place{}, false
			}
			return place{cluster: segs[1]}, true
		case 5: // manifests/<cluster>/<unit>/<phase>/<file>.yaml
			if !layerPhases[segs[3]] {
				v.fail(name, "phase directory %q is not one of pre, rollout, post", segs[3])
				return place{}, false
			}
			c := v.clusterOf(segs[1])
			c.units = true
			if c.files {
				v.fail(name, "cluster %q mixes files directly under it (v1 layout) with unit directories (v2 layout)", segs[1])
				return place{}, false
			}
			return place{cluster: segs[1], unit: segs[2], phase: segs[3]}, true
		}
		v.fail(name, "not in the bundle layout: manifests/<cluster>/<file>.yaml or manifests/<cluster>/<unit>/<phase>/<file>.yaml")
		return place{}, false
	}
	v.fail(name, "not in the bundle layout: top-level directory %q (only manifests/ and observability/)", segs[0])
	return place{}, false
}

func (v *layerValidator) artifact(name string, data []byte) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		v.fail(name, "not valid JSON: %v", err)
		return
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		v.fail(name, "JSON artifact must be an object")
		return
	}
	if !json.Valid(data) {
		v.fail(name, "not valid JSON")
		return
	}
	v.summary.Artifacts = append(v.summary.Artifacts, name)
}

func (v *layerValidator) manifest(name string, p place, data []byte) {
	if len(bytes.TrimSpace(data)) == 0 {
		v.fail(name, "empty file")
		return
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			v.fail(name, "no YAML document")
		} else {
			v.fail(name, "invalid YAML: %v", err)
		}
		return
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		v.fail(name, "more than one YAML document in a file (one object per file)")
		return
	case !errors.Is(err, io.EOF):
		v.fail(name, "invalid YAML after the first document: %v", err)
		return
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		v.fail(name, "not a YAML document")
		return
	}
	root := doc.Content[0]
	if msg := strictNode(root, 0); msg != "" {
		v.fail(name, "%s", msg)
		return
	}
	// Decoding into a value is yaml.v3's own acceptance of the document; it
	// runs only after the walk, which has already refused aliases, so it
	// cannot be made to expand one.
	var decoded any
	if err := root.Decode(&decoded); err != nil {
		v.fail(name, "invalid YAML: %v", err)
		return
	}
	if root.Kind != yaml.MappingNode {
		v.fail(name, "document is not a mapping: a manifest is a Kubernetes object")
		return
	}
	apiVersion, ok := stringField(root, "apiVersion")
	if !ok || apiVersion == "" {
		v.fail(name, "manifest has no apiVersion")
		return
	}
	kind, ok := stringField(root, "kind")
	if !ok || kind == "" {
		v.fail(name, "manifest has no kind")
		return
	}
	meta := mapField(root, "metadata")
	if meta == nil {
		v.fail(name, "manifest has no metadata.name")
		return
	}
	objName, ok := stringField(meta, "name")
	if !ok || objName == "" {
		v.fail(name, "manifest has no metadata.name")
		return
	}
	switch {
	case len(objName) > manifestsLayerMaxNameBytes || hasControl(objName):
		v.fail(name, "metadata.name is over %d bytes or has control characters", manifestsLayerMaxNameBytes)
		return
	case hasControl(kind) || hasControl(apiVersion):
		v.fail(name, "apiVersion or kind has control characters")
		return
	case apiVersion == "kustomize.config.k8s.io" || strings.HasPrefix(apiVersion, "kustomize.config.k8s.io/"):
		v.fail(name, "apiVersion %s is a kustomize configuration, not a Kubernetes object", apiVersion)
		return
	}
	ns, _ := stringField(meta, "namespace")
	v.summary.Objects = append(v.summary.Objects, LayerObject{
		Path: name, Cluster: p.cluster, Unit: p.unit, Phase: p.phase,
		APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: objName,
	})
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// coreTags are the YAML tags a Kubernetes manifest has any use for.
var coreTags = map[string]bool{
	"!!str": true, "!!int": true, "!!float": true, "!!bool": true, "!!null": true,
	"!!map": true, "!!seq": true, "!!timestamp": true,
}

// strictNode refuses what yaml.v3 would otherwise accept silently: anchors and
// aliases (expansion bombs, and a way to make two readers see different
// documents), merge keys, tags outside the core set, duplicate mapping keys,
// and unbounded nesting. It returns "" when the node is acceptable.
func strictNode(n *yaml.Node, depth int) string {
	if depth > manifestsLayerMaxYAMLDepth {
		return fmt.Sprintf("YAML nests deeper than %d levels", manifestsLayerMaxYAMLDepth)
	}
	if n.Kind == yaml.AliasNode {
		return fmt.Sprintf("line %d: YAML aliases are not allowed", n.Line)
	}
	if n.Anchor != "" {
		return fmt.Sprintf("line %d: YAML anchors are not allowed", n.Line)
	}
	if !coreTags[n.ShortTag()] {
		return fmt.Sprintf("line %d: YAML tag %s is not allowed", n.Line, n.Tag)
	}
	if n.Kind == yaml.MappingNode {
		seen := make(map[string]int, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode {
				if k.Value == "<<" && k.ShortTag() != "!!str" {
					return fmt.Sprintf("line %d: YAML merge keys are not allowed", k.Line)
				}
				id := k.ShortTag() + "\x00" + k.Value
				if first, dup := seen[id]; dup {
					return fmt.Sprintf("line %d: duplicate key %q (first at line %d)", k.Line, k.Value, first)
				}
				seen[id] = k.Line
			}
			if msg := strictNode(k, depth+1); msg != "" {
				return msg
			}
			if msg := strictNode(n.Content[i+1], depth+1); msg != "" {
				return msg
			}
		}
		return ""
	}
	for _, c := range n.Content {
		if msg := strictNode(c, depth+1); msg != "" {
			return msg
		}
	}
	return ""
}

// stringField returns the value of key in mapping m when it is a plain string
// scalar. A `name: 123` is !!int, not a name.
func stringField(m *yaml.Node, key string) (string, bool) {
	val := fieldNode(m, key)
	if val == nil || val.Kind != yaml.ScalarNode || val.ShortTag() != "!!str" {
		return "", false
	}
	return val.Value, true
}

func mapField(m *yaml.Node, key string) *yaml.Node {
	val := fieldNode(m, key)
	if val == nil || val.Kind != yaml.MappingNode {
		return nil
	}
	return val
}

func fieldNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
