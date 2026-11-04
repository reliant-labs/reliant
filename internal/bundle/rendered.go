package bundle

// Reading a render stream back as typed objects, for a consumer that needs
// each document's BODY and not the projected shape.
//
// The shape says which objects exist and what they hash to; it deliberately
// carries no body. A caller deciding what a reconciler's identity must be
// allowed to apply (`forge cluster connect`) needs the kinds, namespaces and a
// few spec fields (a CRD's plural, a RoleBinding's roleRef), and it must read
// them from the SAME stream the bundle is built from, through the same
// splitter, so the permission it grants and the objects the bundle applies
// cannot disagree.

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RenderedObject is one document of a render stream.
type RenderedObject struct {
	// Clusters are the clusters the deploy layer routes the document to, from
	// the stream's `# cluster:` header. Empty: attributed to none, so nothing
	// applies it.
	Clusters   []string
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	// Body is the decoded document. It is nil for a Secret: a Secret never
	// rides a bundle and its values are nobody's business here, so the body is
	// not retained.
	Body map[string]any
}

// ParseRenderedStream decodes an annotated render stream into its objects, in
// stream order.
func ParseRenderedStream(stream string) ([]RenderedObject, error) {
	var out []RenderedObject
	for i, doc := range splitStream(stream) {
		var body map[string]any
		if err := yaml.Unmarshal([]byte(doc.yaml), &body); err != nil {
			return nil, fmt.Errorf("manifest document %d does not parse as YAML: %w", i+1, err)
		}
		if body == nil {
			continue
		}
		meta := readMeta(body)
		if meta.kind == "" || meta.name == "" {
			return nil, fmt.Errorf("manifest document %d has no kind or metadata.name", i+1)
		}
		obj := RenderedObject{
			Clusters:   doc.clusters,
			APIVersion: meta.apiVersion,
			Kind:       meta.kind,
			Namespace:  meta.namespace,
			Name:       meta.name,
			Body:       body,
		}
		if meta.kind == "Secret" {
			obj.Body = nil
		}
		out = append(out, obj)
	}
	return out, nil
}
