// Package eso holds what forge pins about the External Secrets Operator
// component (forge.ExternalSecretsOperator): the chart and image it installs,
// the keyless-signing identity that image must carry, and the CRDs forge
// supplies itself.
//
// The values here are the Go half of kcl/schema.k (ESO_CHART_VERSION,
// ESO_IMAGE) and kcl/lib/eso.k; TestKCLPinsAgree pins them against each other.
//
// forge supplies ONLY the CRDs the hardened install uses (ExternalSecret,
// SecretStore and GeneratorState). The chart is rendered `--skip-crds` AND with
// installCRDs=false: in chart 2.13 the CRDs are ordinary templates gated on
// `installCRDs`, so `--skip-crds` alone would not exclude them. The kinds the
// install does not process (ClusterSecretStore, ClusterExternalSecret,
// PushSecret, ClusterPushSecret, ClusterGenerator and the generators) have no
// CRD on the cluster, so nothing can create one.
package eso

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ChartOCI is the chart's OCI reference.
	ChartOCI = "oci://ghcr.io/external-secrets/charts/external-secrets"
	// ChartVersion is the pinned chart version. Chart 2.13.0 is appVersion
	// v2.13.0; the CRD bundle below is rendered from exactly this chart.
	ChartVersion = "2.13.0"
	// ChartName is the `--target` selector of the HelmChart forge declares.
	ChartName = "external-secrets"
	// Namespace is the default namespace the operator runs in.
	Namespace = "external-secrets"

	// ImageRepository and ImageTag are the controller image's reference; the
	// digest it is pinned to lives in kcl/schema.k (ESO_IMAGE).
	ImageRepository = "ghcr.io/external-secrets/external-secrets"
	ImageTag        = "v2.13.0"

	// CosignIssuer and CosignIdentity are the keyless-signing identity a
	// controller image must carry (P2). The certificate's subject
	// alternative name is the release workflow at the main branch, issued by
	// GitHub Actions' OIDC provider. Verified for the pinned digest with
	//
	//	cosign verify \
	//	  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
	//	  --certificate-identity https://github.com/external-secrets/external-secrets/.github/workflows/release.yml@refs/heads/main \
	//	  ghcr.io/external-secrets/external-secrets@sha256:<digest>
	CosignIssuer   = "https://token.actions.githubusercontent.com"
	CosignIdentity = "https://github.com/external-secrets/external-secrets/.github/workflows/release.yml@refs/heads/main"
)

// ComponentOwnerLabel is stamped on every object the component renders. Its
// value is the owner (<project>.<env>), so a second env that declares the
// component on the same cluster is found by label, never by name.
const ComponentOwnerLabel = "forge.dev/component-owner"

// ComponentLabel names the component an object belongs to.
const ComponentLabel = "forge.dev/component"

// ComponentName is ComponentLabel's value for this component.
const ComponentName = "external-secrets-operator"

// CRDGroups are the API groups of the CRDs forge supplies.
var CRDGroups = []string{"external-secrets.io", "generators.external-secrets.io"}

// CRDNames are the CustomResourceDefinitions forge supplies, in apply order.
var CRDNames = []string{
	"externalsecrets.external-secrets.io",
	"secretstores.external-secrets.io",
	"generatorstates.generators.external-secrets.io",
}

//go:embed crds/*/*.yaml
var crdFS embed.FS

// CRDs returns the CRD bundle for a chart version: the three CRDs the
// hardened install uses, `---`-separated, in CRDNames order, each stamped with
// ComponentLabel and (when owner is not empty) ComponentOwnerLabel. A version
// forge holds no bundle for is an error, not a fallback: the CRDs and the
// controller move together.
func CRDs(version, owner string) (string, error) {
	dir := path.Join("crds", version)
	entries, err := fs.ReadDir(crdFS, dir)
	if err != nil {
		return "", fmt.Errorf("no External Secrets Operator CRD bundle for chart version %q (forge holds %s): the CRDs are pinned to the chart, so bump them together", version, strings.Join(Versions(), ", "))
	}
	byFile := map[string]string{}
	for _, e := range entries {
		b, err := fs.ReadFile(crdFS, path.Join(dir, e.Name()))
		if err != nil {
			return "", err
		}
		byFile[strings.TrimSuffix(e.Name(), ".yaml")] = strings.TrimRight(string(b), "\n")
	}
	docs := make([]string, 0, len(CRDNames))
	for _, n := range CRDNames {
		plural, _, _ := strings.Cut(n, ".")
		d, ok := byFile[plural]
		if !ok {
			return "", fmt.Errorf("CRD bundle %s is missing %s", version, n)
		}
		stamped, err := stampLabels(d, owner)
		if err != nil {
			return "", fmt.Errorf("CRD bundle %s: %s: %w", version, n, err)
		}
		docs = append(docs, stamped)
	}
	return strings.Join(docs, "\n---\n") + "\n", nil
}

// stampLabels adds the component labels to a CRD document by editing its
// yaml.Node tree, so every other key keeps its position and the 700 KiB
// document diffs as the added lines only.
func stampLabels(doc, owner string) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil || len(root.Content) == 0 {
		return "", fmt.Errorf("parse: %v", err)
	}
	meta := child(root.Content[0], "metadata")
	labels := child(meta, "labels")
	set(labels, ComponentLabel, ComponentName)
	if owner != "" {
		set(labels, ComponentOwnerLabel, owner)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func child(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.MappingNode})
	return m.Content[len(m.Content)-1]
}

func set(m *yaml.Node, key, value string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: value}
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

// Versions lists the chart versions forge holds a CRD bundle for.
func Versions() []string {
	entries, _ := fs.ReadDir(crdFS, "crds")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
