package release

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/secretgen"
)

// Projecting a render into the store-secret fields of a [Shape].
//
// The input types below are the render output's own records, with the render's
// own JSON names, so a caller decodes `output.required_secrets[]`,
// `output.openbao` and `output.external_secrets_operators[]` straight into
// them and nothing here depends on how those records were produced. The one
// thing a render cannot say is which clusters a namespace's Secret lands on:
// that is the deploy router's answer (the same attribution the preflight
// uses), so the caller resolves it and passes it in
// [RenderedStoreSecret.Clusters]. A component record carries its own resolved
// cluster, as `control_plane_secrets` entries do.

// StoreRender is everything [ProjectStoreSecrets] reads.
type StoreRender struct {
	// Secrets are the render's declared ExternalSecrets. Those without a
	// Source are out of band and project to nothing.
	Secrets                  []RenderedStoreSecret
	OpenBao                  *RenderedOpenBao
	ExternalSecretsOperators []RenderedExternalSecretsOperator
}

// RenderedStoreSecret is one `required_secrets` record.
type RenderedStoreSecret struct {
	Name      string               `json:"name"`
	Namespace string               `json:"namespace"`
	Source    *RenderedStoreSource `json:"source,omitempty"`
	// Clusters are the clusters the Secret is delivered to, resolved by the
	// caller. Not part of the render record.
	Clusters []StoreCluster `json:"-"`
}

// StoreCluster names a cluster the way the shape's other lists do.
type StoreCluster struct {
	// Cluster is the kube context.
	Cluster string
	// ConnectedCluster is the name `forge cluster connect` registered; empty
	// when no control plane reaches the cluster.
	ConnectedCluster string
}

// RenderedStoreSource is a rendered forge.StoreSource.
type RenderedStoreSource struct {
	Store     RenderedStore                      `json:"store"`
	Data      map[string]string                  `json:"data,omitempty"`
	Generate  map[string]RenderedGeneratedSecret `json:"generate,omitempty"`
	Pin       map[string]int                     `json:"pin,omitempty"`
	Immutable []string                           `json:"immutable,omitempty"`
}

// RenderedStore is a rendered store kind. Type is the store's discriminator
// ("hosted", "openbao", "gcpsm").
type RenderedStore struct {
	Type    string `json:"type"`
	Project string `json:"project,omitempty"`
}

// RenderedGeneratedSecret is a rendered forge.GeneratedSecret. The render
// emits the schema defaults for the parameters of kinds the entry is not, so
// the projection keeps only the ones its kind reads.
type RenderedGeneratedSecret struct {
	Kind       string `json:"kind"`
	Bytes      int    `json:"bytes,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	Length     int    `json:"length,omitempty"`
	Symbols    string `json:"symbols,omitempty"`
	Companion  string `json:"companion,omitempty"`
	CommonName string `json:"common_name,omitempty"`
	ValidDays  int    `json:"valid_days,omitempty"`
}

// RenderedOpenBao is the rendered `openbao` record.
type RenderedOpenBao struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	Image            string `json:"image"`
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster,omitempty"`
}

// RenderedExternalSecretsOperator is one rendered `external_secrets_operators`
// record.
type RenderedExternalSecretsOperator struct {
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster,omitempty"`
	Namespace        string `json:"namespace"`
	Version          string `json:"version"`
}

// StoreProjection is the four store-secret fields of a [Shape], as one value a
// builder passes around and [Shape.WithStoreSecrets] applies.
type StoreProjection struct {
	StoreSecrets             []ShapeStoreSecret
	GeneratedSecrets         []ShapeGeneratedSecret
	OpenBao                  *ShapeOpenBao
	ExternalSecretsOperators []ShapeExternalSecretsOperator
}

// WithStoreSecrets returns the shape with the projection's fields set.
func (s Shape) WithStoreSecrets(p StoreProjection) Shape {
	s.StoreSecrets = p.StoreSecrets
	s.GeneratedSecrets = p.GeneratedSecrets
	s.OpenBao = p.OpenBao
	s.ExternalSecretsOperators = p.ExternalSecretsOperators
	return s
}

// ProjectStoreSecrets projects a render into the shape's store-secret fields.
//
// It is deterministic: the same render gives the same projection, whatever
// order the records arrived in, and the result is canonical. A render with no
// store-backed ExternalSecret and no component projects to the zero
// [StoreProjection], which encodes as nothing.
//
// It refuses what would make the shape ambiguous rather than picking a winner:
// a store-backed Secret that reaches no cluster (there is no role to derive for
// it), two sources that disagree about a pin or about how a name is generated,
// and a value generated into Google Secret Manager.
func ProjectStoreSecrets(in StoreRender) (StoreProjection, error) {
	var out StoreProjection

	if o := in.OpenBao; o != nil {
		out.OpenBao = &ShapeOpenBao{Name: o.Name, Namespace: o.Namespace, Cluster: o.Cluster, ConnectedCluster: o.ConnectedCluster, Image: o.Image}
	}
	for _, e := range in.ExternalSecretsOperators {
		out.ExternalSecretsOperators = append(out.ExternalSecretsOperators, ShapeExternalSecretsOperator{
			Cluster: e.Cluster, ConnectedCluster: e.ConnectedCluster, Namespace: e.Namespace, Version: e.Version,
		})
	}

	bindings := map[string]*ShapeStoreSecret{}
	var order []string
	byName := map[string]ShapeGeneratedSecret{}

	for _, sec := range in.Secrets {
		src := sec.Source
		if src == nil {
			continue
		}
		where := fmt.Sprintf("external secret %s/%s", sec.Namespace, sec.Name)
		store := src.Store.Type
		if !containsString(StoreKinds, store) {
			return StoreProjection{}, fmt.Errorf("%w: %s: store %q (expected one of %s)", ErrInvalid, where, store, strings.Join(StoreKinds, ", "))
		}
		if len(sec.Clusters) == 0 {
			return StoreProjection{}, fmt.Errorf("%w: %s reads from a store and reaches no cluster, so no role can be derived for it", ErrInvalid, where)
		}

		read := map[string]bool{}
		for _, key := range sortedMapKeys(src.Data) {
			read[src.Data[key]] = true
		}

		for _, c := range sec.Clusters {
			key := strings.Join([]string{c.Cluster, sec.Namespace, store, src.Store.Project}, "/")
			b := bindings[key]
			if b == nil {
				b = &ShapeStoreSecret{Cluster: c.Cluster, ConnectedCluster: c.ConnectedCluster, Namespace: sec.Namespace, Store: store, Project: src.Store.Project}
				bindings[key] = b
				order = append(order, key)
			} else if b.ConnectedCluster != c.ConnectedCluster {
				return StoreProjection{}, fmt.Errorf("%w: %s: cluster %q is bound to connected cluster %q here and %q elsewhere", ErrInvalid, where, c.Cluster, c.ConnectedCluster, b.ConnectedCluster)
			}
			for name := range read {
				b.Names = appendUnique(b.Names, name)
			}
			for _, name := range sortedMapKeys(src.Pin) {
				if !read[name] {
					continue
				}
				if err := b.pin(name, src.Pin[name]); err != nil {
					return StoreProjection{}, fmt.Errorf("%w: %s: %v", ErrInvalid, where, err)
				}
			}
			for _, name := range src.Immutable {
				if read[name] {
					b.Immutable = appendUnique(b.Immutable, name)
				}
			}
		}

		for _, name := range sortedMapKeys(src.Generate) {
			g, err := src.Generate[name].shape(store, name)
			if err != nil {
				return StoreProjection{}, fmt.Errorf("%w: %s: %v", ErrInvalid, where, err)
			}
			g = g.canonical()
			for _, n := range g.Names {
				if prev, ok := byName[g.Store+"/"+n]; ok && !sameGeneration(prev, g) {
					return StoreProjection{}, fmt.Errorf("%w: %s generates %q into the %s store differently from another source", ErrInvalid, where, n, g.Store)
				}
			}
			if _, ok := byName[g.Store+"/"+g.Names[0]]; ok {
				continue
			}
			for _, n := range g.Names {
				byName[g.Store+"/"+n] = g
			}
			out.GeneratedSecrets = append(out.GeneratedSecrets, g)
		}
	}

	for _, key := range order {
		out.StoreSecrets = append(out.StoreSecrets, *bindings[key])
	}

	// Canonical sorts and normalizes; run it here so a caller that records the
	// projection without going through a Shape still gets canonical lists.
	c := Shape{StoreSecrets: out.StoreSecrets, GeneratedSecrets: out.GeneratedSecrets, ExternalSecretsOperators: out.ExternalSecretsOperators}
	var canon Shape
	c.canonicalStoreSecrets(&canon)
	out.StoreSecrets, out.GeneratedSecrets, out.ExternalSecretsOperators = canon.StoreSecrets, canon.GeneratedSecrets, canon.ExternalSecretsOperators
	return out, nil
}

// pin records a version for a name, refusing a second, different one.
func (b *ShapeStoreSecret) pin(name string, version int) error {
	for _, p := range b.Pins {
		if p.Name == name {
			if p.Version != version {
				return fmt.Errorf("%q is pinned to version %d and to version %d in the same namespace", name, p.Version, version)
			}
			return nil
		}
	}
	b.Pins = append(b.Pins, ShapeStorePin{Name: name, Version: version})
	return nil
}

// shape turns a rendered generate entry into the shape's, naming where it is
// minted and the names it writes (the key first, then the companion).
func (g RenderedGeneratedSecret) shape(store, name string) (ShapeGeneratedSecret, error) {
	switch store {
	case StoreHosted, StoreOpenBao:
	default:
		return ShapeGeneratedSecret{}, fmt.Errorf("%q is generated into the %s store, which no one mints into (the project's owner writes those values)", name, store)
	}
	kind := secretgen.Kind(g.Kind)
	names := []string{name}
	switch kind.Arity() {
	case 0:
		return ShapeGeneratedSecret{}, fmt.Errorf("%q: unknown generated kind %q (kinds: %v)", name, g.Kind, secretgen.Kinds())
	case 2:
		if g.Companion == "" {
			return ShapeGeneratedSecret{}, fmt.Errorf("%q: kind %s writes two names and has no companion", name, g.Kind)
		}
		names = append(names, g.Companion)
	}
	return ShapeGeneratedSecret{
		Store: store, Kind: g.Kind, Names: names,
		Bytes: g.Bytes, Encoding: g.Encoding, Prefix: g.Prefix,
		Length: g.Length, Symbols: g.Symbols,
		CommonName: g.CommonName, ValidDays: g.ValidDays,
	}, nil
}

func sameGeneration(a, b ShapeGeneratedSecret) bool {
	a, b = a.canonical(), b.canonical()
	return a.Store == b.Store && a.Kind == b.Kind && strings.Join(a.Names, "\x00") == strings.Join(b.Names, "\x00") &&
		a.Bytes == b.Bytes && a.Encoding == b.Encoding && a.Prefix == b.Prefix &&
		a.Length == b.Length && a.Symbols == b.Symbols && a.CommonName == b.CommonName && a.ValidDays == b.ValidDays
}

func appendUnique(list []string, s string) []string {
	if containsString(list, s) {
		return list
	}
	return append(list, s)
}
