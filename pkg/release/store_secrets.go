package release

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/secretaccess"
	"github.com/reliant-labs/forge/pkg/secretgen"
)

// Store-backed secrets in the release shape.
//
// A forge.ExternalSecret with a forge.StoreSource is a Secret that ESO syncs
// from a secret store. The shape records what a control plane needs to serve
// it, and nothing else:
//
//   - [Shape.StoreSecrets]: per (cluster, namespace, store), the store NAMES the
//     namespace reads, with their version pins. Feeds [StoreBindings], and so a
//     secretaccess.Request: the exact-path policies and per-binding roles;
//   - [Shape.GeneratedSecrets]: the values to mint when the store has none
//     (secretgen.Spec-compatible), minted at promotion before the policy widens;
//   - [Shape.OpenBao] and [Shape.ExternalSecretsOperators]: which clusters
//     declare the components, so a promotion can check the CRDs and the owner.
//
// This is the contract control-plane's storeaccess and secretmint consume.
// Like everything in a shape it names things and never carries a value: a
// generated secret is a recipe, a pin is a version number. All four fields are
// omitted when empty, so a shape recorded before them encodes and hashes as it
// did, and a reader that predates them gets none
// ([BundleFeatureStoreSecrets], [BundleReader.LimitShape]).

// Store kinds a binding reads from ([ShapeStoreSecret.Store]).
const (
	// StoreHosted is the secret store the env's hosted control plane runs.
	StoreHosted = "hosted"
	// StoreOpenBao is the OpenBao the env runs itself (Bundle.openbao).
	StoreOpenBao = "openbao"
	// StoreGCPSecretManager is Google Secret Manager. Its values are written
	// by the project's owner, so a binding to it derives no store policy and
	// carries no generated secret.
	StoreGCPSecretManager = "gcpsm"
)

// StoreKinds is every store kind a binding can name.
var StoreKinds = []string{StoreHosted, StoreOpenBao, StoreGCPSecretManager}

// ShapeStoreSecret is the set of store names one namespace of one cluster
// reads from one store: the unit secretaccess derives a role and a policy for.
// Several ExternalSecrets of the namespace merge into it.
type ShapeStoreSecret struct {
	// Cluster is the kube context the namespace lives in, the same key the
	// shape's workloads and objects use.
	Cluster string `json:"cluster"`
	// ConnectedCluster is the name `forge cluster connect` registered, which
	// a control plane resolves to the cluster's auth mount. Empty for a
	// cluster no control plane reaches (a self-run store's converge Job
	// addresses the cluster by Cluster).
	ConnectedCluster string `json:"connected_cluster,omitempty"`
	Namespace        string `json:"namespace"`
	// Store is the store kind ([StoreHosted], [StoreOpenBao],
	// [StoreGCPSecretManager]).
	Store string `json:"store"`
	// Project is the Google project of a [StoreGCPSecretManager] binding, and
	// empty for the others.
	Project string `json:"project,omitempty"`
	// Names are the store names the namespace reads, sorted and distinct:
	// exactly the values of its ExternalSecrets' `data`. A name a source only
	// generates and no ExternalSecret reads is not here.
	Names []string `json:"names"`
	// Pins are the KV versions the namespace reads. A name with no pin
	// floats on the latest version.
	Pins []ShapeStorePin `json:"pins,omitempty"`
	// Immutable are names that are never rotated in place; a pin above 1 on
	// one is invalid.
	Immutable []string `json:"immutable,omitempty"`
}

// ShapeStorePin pins one store name to a KV version.
type ShapeStorePin struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Key identifies the binding within a shape.
func (s ShapeStoreSecret) Key() string {
	return strings.Join([]string{s.Cluster, s.Namespace, s.Store, s.Project}, "/")
}

// ShapeGeneratedSecret is one value (or one atomic pair) to mint when the
// store holds none. Its fields are secretgen.Spec's, with the lifetime in days
// so the shape stays JSON-plain; [ShapeGeneratedSecret.Spec] converts. It is
// minted once per store name and never overwritten.
type ShapeGeneratedSecret struct {
	// Store says who mints it: [StoreHosted] (the control plane, at promotion)
	// or [StoreOpenBao] (the component's converge Job, inside the cluster).
	Store string `json:"store"`
	// Kind is a secretgen.Kind.
	Kind string `json:"kind"`
	// Names are the store names written, in secretgen's order: the key first,
	// then its companion for the two-name kinds. Not sorted: the order is
	// meaningful.
	Names []string `json:"names"`

	// Parameters of the kinds that take them; the others carry zero values.
	// Canonical fills the defaults, so a shape that spells them out and one
	// that leaves them to the default are one shape.
	Bytes      int    `json:"bytes,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	Length     int    `json:"length,omitempty"`
	Symbols    string `json:"symbols,omitempty"`
	CommonName string `json:"common_name,omitempty"`
	ValidDays  int    `json:"valid_days,omitempty"`
}

// Spec is the secretgen.Spec this entry stands for.
func (g ShapeGeneratedSecret) Spec() secretgen.Spec {
	return secretgen.Spec{
		Kind:       secretgen.Kind(g.Kind),
		Names:      append([]string(nil), g.Names...),
		Bytes:      g.Bytes,
		Encoding:   g.Encoding,
		Prefix:     g.Prefix,
		Length:     g.Length,
		Symbols:    g.Symbols,
		CommonName: g.CommonName,
		ValidFor:   time.Duration(g.ValidDays) * 24 * time.Hour,
	}
}

func (g ShapeGeneratedSecret) canonical() ShapeGeneratedSecret {
	out := g
	out.Names = append([]string(nil), g.Names...)
	out.Bytes, out.Encoding, out.Prefix = 0, "", ""
	out.Length, out.Symbols = 0, ""
	out.CommonName, out.ValidDays = "", 0
	switch secretgen.Kind(g.Kind) {
	case secretgen.KindRandom:
		out.Bytes, out.Encoding, out.Prefix = g.Bytes, g.Encoding, g.Prefix
		if out.Bytes == 0 {
			out.Bytes = secretgen.DefaultBytes
		}
		if out.Encoding == "" {
			out.Encoding = secretgen.DefaultEncoding
		}
	case secretgen.KindPassword:
		out.Length, out.Symbols = g.Length, g.Symbols
		if out.Length == 0 {
			out.Length = secretgen.DefaultLength
		}
		if out.Symbols == "" {
			out.Symbols = secretgen.DefaultSymbols
		}
	case secretgen.KindECP256SelfSigned:
		out.CommonName, out.ValidDays = g.CommonName, g.ValidDays
		if out.ValidDays == 0 {
			out.ValidDays = int(secretgen.DefaultValidFor / (24 * time.Hour))
		}
	}
	return out
}

// ShapeOpenBao records that the env declares the self-run OpenBao component
// (Bundle.openbao), and where. One per env; the component is single-owner per
// cluster, which the control plane checks across envs.
type ShapeOpenBao struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Cluster is the kube context it runs in; ConnectedCluster as for
	// [ShapeStoreSecret.ConnectedCluster].
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster,omitempty"`
	// Image is the pinned image reference, so a promotion can tell a version
	// change from a configuration change.
	Image string `json:"image"`
}

// ShapeExternalSecretsOperator records that the env declares the ESO
// component on a cluster (Bundle.external_secrets_operators): what a
// promotion needs to check that the CRDs exist and that this env owns the
// install. One per cluster.
type ShapeExternalSecretsOperator struct {
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster,omitempty"`
	Namespace        string `json:"namespace"`
	// Version is the chart version.
	Version string `json:"version"`
}

// StoreBindings returns the bindings secretaccess derives roles and policies
// from, for one store kind ([StoreHosted] or [StoreOpenBao]): the namespaces
// of that store and the names each reads, by exact path.
//
// A binding's cluster is its ConnectedCluster when set, else its Cluster; a
// control plane that keys its mounts by another id maps from there. Wildcards
// are never produced: a shape cannot ask for one. The result is sorted by
// (cluster, namespace). A [StoreGCPSecretManager] binding derives nothing, and
// asking for it returns none.
func (s Shape) StoreBindings(store string) []secretaccess.Binding {
	if store != StoreHosted && store != StoreOpenBao {
		return nil
	}
	merged := map[[2]string]map[string]bool{}
	for _, b := range s.StoreSecrets {
		if b.Store != store {
			continue
		}
		cluster := b.ConnectedCluster
		if cluster == "" {
			cluster = b.Cluster
		}
		key := [2]string{cluster, b.Namespace}
		if merged[key] == nil {
			merged[key] = map[string]bool{}
		}
		for _, n := range b.Names {
			merged[key][n] = true
		}
	}
	keys := make([][2]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := make([]secretaccess.Binding, 0, len(keys))
	for _, k := range keys {
		names := make([]string, 0, len(merged[k]))
		for n := range merged[k] {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, secretaccess.Binding{Cluster: k[0], Namespace: k[1], Names: names})
	}
	return out
}

// canonicalStoreSecrets sorts the store-secret fields of a shape. Order inside
// a generated secret's Names is meaningful and kept.
func (s Shape) canonicalStoreSecrets(out *Shape) {
	if len(s.StoreSecrets) > 0 {
		out.StoreSecrets = make([]ShapeStoreSecret, len(s.StoreSecrets))
		for i, b := range s.StoreSecrets {
			b.Names = sortedStrings(b.Names)
			b.Immutable = sortedStrings(b.Immutable)
			if len(b.Pins) > 0 {
				b.Pins = append([]ShapeStorePin(nil), b.Pins...)
				sort.Slice(b.Pins, func(i, j int) bool { return b.Pins[i].Name < b.Pins[j].Name })
			}
			out.StoreSecrets[i] = b
		}
		sort.Slice(out.StoreSecrets, func(i, j int) bool { return out.StoreSecrets[i].Key() < out.StoreSecrets[j].Key() })
	}
	if len(s.GeneratedSecrets) > 0 {
		out.GeneratedSecrets = make([]ShapeGeneratedSecret, len(s.GeneratedSecrets))
		for i, g := range s.GeneratedSecrets {
			out.GeneratedSecrets[i] = g.canonical()
		}
		sort.Slice(out.GeneratedSecrets, func(i, j int) bool {
			a, b := out.GeneratedSecrets[i], out.GeneratedSecrets[j]
			if a.Store != b.Store {
				return a.Store < b.Store
			}
			return strings.Join(a.Names, "\x00") < strings.Join(b.Names, "\x00")
		})
	}
	if len(s.ExternalSecretsOperators) > 0 {
		out.ExternalSecretsOperators = append([]ShapeExternalSecretsOperator(nil), s.ExternalSecretsOperators...)
		sort.Slice(out.ExternalSecretsOperators, func(i, j int) bool {
			return out.ExternalSecretsOperators[i].Cluster < out.ExternalSecretsOperators[j].Cluster
		})
	}
}

// validateStoreSecrets is the shape-side check of the four fields. KCL
// refuses most of it at render; the shape re-checks because it is stored
// forever and read by a different program than the one that wrote it.
func (s Shape) validateStoreSecrets() error {
	seen := map[string]bool{}
	for _, b := range s.StoreSecrets {
		where := "shape store secret " + b.Key()
		switch {
		case strings.TrimSpace(b.Cluster) == "" || strings.TrimSpace(b.Namespace) == "":
			return fmt.Errorf("%w: shape store secret needs a cluster and a namespace", ErrInvalid)
		case !containsString(StoreKinds, b.Store):
			return fmt.Errorf("%w: %s: store %q (expected one of %s)", ErrInvalid, where, b.Store, strings.Join(StoreKinds, ", "))
		case (b.Store == StoreGCPSecretManager) != (b.Project != ""):
			return fmt.Errorf("%w: %s: a project belongs to the %s store and to no other", ErrInvalid, where, StoreGCPSecretManager)
		case len(b.Names) == 0:
			return fmt.Errorf("%w: %s reads no names", ErrInvalid, where)
		case seen[b.Key()]:
			return fmt.Errorf("%w: shape names store secret %s twice; merge the names", ErrInvalid, where)
		case b.Store == StoreOpenBao && s.OpenBao == nil:
			return fmt.Errorf("%w: %s reads from the env's OpenBao, and the shape declares none", ErrInvalid, where)
		}
		seen[b.Key()] = true
		names := map[string]bool{}
		for _, n := range b.Names {
			if b.Store != StoreGCPSecretManager && !secretaccess.ValidName(n) {
				return fmt.Errorf("%w: %s: %q is not a valid secret name", ErrInvalid, where, n)
			}
			if strings.TrimSpace(n) == "" {
				return fmt.Errorf("%w: %s: an empty name", ErrInvalid, where)
			}
			if names[n] {
				return fmt.Errorf("%w: %s names %q twice", ErrInvalid, where, n)
			}
			names[n] = true
		}
		pinned := map[string]bool{}
		for _, p := range b.Pins {
			switch {
			case !names[p.Name]:
				return fmt.Errorf("%w: %s pins %q, which it does not read", ErrInvalid, where, p.Name)
			case p.Version < 1:
				return fmt.Errorf("%w: %s pins %q to version %d (a pin is 1 or more; leave a floating name unpinned)", ErrInvalid, where, p.Name, p.Version)
			case pinned[p.Name]:
				return fmt.Errorf("%w: %s pins %q twice", ErrInvalid, where, p.Name)
			}
			pinned[p.Name] = true
		}
		immutable := map[string]bool{}
		for _, n := range b.Immutable {
			if !names[n] {
				return fmt.Errorf("%w: %s marks %q immutable, which it does not read", ErrInvalid, where, n)
			}
			if immutable[n] {
				return fmt.Errorf("%w: %s marks %q immutable twice", ErrInvalid, where, n)
			}
			immutable[n] = true
		}
		for _, p := range b.Pins {
			if immutable[p.Name] && p.Version > 1 {
				return fmt.Errorf("%w: %s pins immutable %q to version %d (an immutable name is never rotated in place)", ErrInvalid, where, p.Name, p.Version)
			}
		}
	}

	minted := map[string]bool{}
	for _, g := range s.GeneratedSecrets {
		if g.Store != StoreHosted && g.Store != StoreOpenBao {
			return fmt.Errorf("%w: shape generated secret %v: store %q (expected %s or %s; no one else mints into %s)", ErrInvalid, g.Names, g.Store, StoreHosted, StoreOpenBao, StoreGCPSecretManager)
		}
		if g.Store == StoreOpenBao && s.OpenBao == nil {
			return fmt.Errorf("%w: shape generated secret %v is minted into the env's OpenBao, and the shape declares none", ErrInvalid, g.Names)
		}
		if err := g.Spec().Validate(); err != nil {
			return fmt.Errorf("%w: shape generated secret %v: %v", ErrInvalid, g.Names, err)
		}
		for _, n := range g.Names {
			k := g.Store + "/" + n
			if minted[k] {
				return fmt.Errorf("%w: shape generates %q into the %s store twice", ErrInvalid, n, g.Store)
			}
			minted[k] = true
		}
	}

	if o := s.OpenBao; o != nil {
		if strings.TrimSpace(o.Name) == "" || strings.TrimSpace(o.Namespace) == "" || strings.TrimSpace(o.Cluster) == "" || strings.TrimSpace(o.Image) == "" {
			return fmt.Errorf("%w: shape openbao needs a name, a namespace, a cluster and an image", ErrInvalid)
		}
	}
	clusters := map[string]bool{}
	for _, e := range s.ExternalSecretsOperators {
		if strings.TrimSpace(e.Cluster) == "" || strings.TrimSpace(e.Namespace) == "" || strings.TrimSpace(e.Version) == "" {
			return fmt.Errorf("%w: shape external-secrets operator needs a cluster, a namespace and a version", ErrInvalid)
		}
		if clusters[e.Cluster] {
			return fmt.Errorf("%w: shape installs the external-secrets operator twice on cluster %q (one per cluster)", ErrInvalid, e.Cluster)
		}
		clusters[e.Cluster] = true
	}
	return nil
}
