package bundle

// Reading a bundle's objects back, and naming the fields two of them differ in.
//
// A shape says THAT an object changed — one hash per object, which is all a
// plan needs to classify the change. A reviewer approving the plan needs to
// know WHAT changed, and "config_changed prod/Deployment/app/api" sends them
// off to diff two renders by hand. The manifest layer holds every object the
// shape describes, so the fields can be named from the two bundles themselves.
//
// PATHS, NEVER VALUES. A field summary is printed into a plan, and a plan is
// printed into CI logs. The layer carries no Secret (a Secret never rides it,
// not even redacted — see release.BundleDoc.Secrets), but a ConfigMap or an
// env var value is still nobody's business in a log line, and a path is what
// a reviewer needs to go and look.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// Objects reads a fetched bundle's manifest layer into its documents, keyed
// exactly as the bundle's shape keys its objects (cluster, kind, namespace,
// name), so a plan finding's subject finds its document.
//
// The cluster comes from the tree the document sits in, as the bundle's own
// ClusterPaths names it; a file outside every named tree is skipped rather
// than attributed to a guessed cluster. It applies Unpack's bounds — the
// layer is as hostile here as anywhere else.
func Objects(f Fetched) (map[release.ObjectKey]any, error) {
	clusterOf := map[string]string{}
	for _, t := range f.Doc.ClusterPaths {
		clusterOf[t.Path] = t.Cluster
	}
	gz, err := gzip.NewReader(bytes.NewReader(f.Manifests))
	if err != nil {
		return nil, fmt.Errorf("bundle %s manifest layer: %w", f.Digest, err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(io.LimitReader(gz, MaxLayerBytes))
	out := map[release.ObjectKey]any{}
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("bundle %s manifest layer: %w", f.Digest, err)
		}
		if entries >= MaxEntries {
			return nil, fmt.Errorf("%w: bundle %s holds more than %d entries", ErrUnsafeArchive, f.Digest, MaxEntries)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > MaxFileBytes {
			return nil, fmt.Errorf("%w: bundle %s entry %s is %d bytes", ErrUnsafeArchive, f.Digest, h.Name, h.Size)
		}
		cluster, ok := clusterOfEntry(clusterOf, h.Name)
		if !ok {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("bundle %s entry %s: %w", f.Digest, h.Name, err)
		}
		var body any
		if err := yaml.Unmarshal(data, &body); err != nil {
			return nil, fmt.Errorf("bundle %s entry %s does not parse: %w", f.Digest, h.Name, err)
		}
		meta := readMeta(body)
		out[release.ObjectKey{Cluster: cluster, Kind: meta.kind, Namespace: meta.namespace, Name: meta.name}] = body
	}
}

// ChangedFields names every field two documents differ in, as dotted paths,
// sorted. A subtree present on one side only is named once, at its root.
//
// A list whose items are all objects carrying a string `name` — containers,
// env vars, ports, volumes — is compared BY NAME and its items named by it
// (`spec.template.spec.containers[api].image`): an index would shift when an
// item is inserted, and every later item would read as changed.
func ChangedFields(live, candidate any) []string {
	var out []string
	diffFields("", live, candidate, &out)
	sort.Strings(out)
	return out
}

func diffFields(at string, a, b any, out *[]string) {
	if am, ok := a.(map[string]any); ok {
		if bm, ok := b.(map[string]any); ok {
			keys := make([]string, 0, len(am)+len(bm))
			for k := range am {
				keys = append(keys, k)
			}
			for k := range bm {
				if _, dup := am[k]; !dup {
					keys = append(keys, k)
				}
			}
			for _, k := range keys {
				diffFields(joinField(at, k), am[k], bm[k], out)
			}
			return
		}
	}
	if al, ok := a.([]any); ok {
		if bl, ok := b.([]any); ok {
			diffLists(at, al, bl, out)
			return
		}
	}
	if !reflect.DeepEqual(a, b) {
		if at == "" {
			at = "."
		}
		*out = append(*out, at)
	}
}

func diffLists(at string, a, b []any, out *[]string) {
	an, aNamed := byName(a)
	bn, bNamed := byName(b)
	if aNamed && bNamed {
		seen := map[string]bool{}
		for _, list := range [][]any{a, b} {
			for _, item := range list {
				name := item.(map[string]any)["name"].(string)
				if !seen[name] {
					seen[name] = true
					diffFields(at+"["+name+"]", an[name], bn[name], out)
				}
			}
		}
		return
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var ai, bi any
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		diffFields(at+"["+strconv.Itoa(i)+"]", ai, bi, out)
	}
}

// byName indexes a list whose every item is an object with a distinct string
// `name`; ok is false for any other list, which is then compared by index.
func byName(list []any) (map[string]any, bool) {
	if len(list) == 0 {
		return map[string]any{}, true
	}
	out := make(map[string]any, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, false
		}
		if _, dup := out[name]; dup {
			return nil, false
		}
		out[name] = m
	}
	return out, true
}

func joinField(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// clusterOfEntry is the cluster a manifest file belongs to: the cluster path
// is the first two segments of every entry, in both the flat layout
// (manifests/<cluster>/<file>) and the tree layout
// (manifests/<cluster>/<unit>/<phase>/<file>).
func clusterOfEntry(clusterOf map[string]string, name string) (string, bool) {
	segs := strings.SplitN(path.Clean(name), "/", 3)
	if len(segs) < 3 {
		return "", false
	}
	cluster, ok := clusterOf[segs[0]+"/"+segs[1]]
	return cluster, ok
}
