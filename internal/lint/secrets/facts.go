// Package secrets is the `forge lint --secrets` lane: the rules about platform
// secrets (forge.ExternalSecret sources, External Secrets Operator objects,
// OpenBao grants) that the KCL schema cannot see because they span a whole
// rendered environment, or because they judge the objects it renders.
//
// The package is pure. It judges [Env] values the caller built from a render
// (internal/cli/lint does the rendering) and reports finding.Finding values
// anchored at file:line in the project's KCL, found by a text [Locator].
//
// THE LANE IS OPT-IN BY ADOPTION. An environment is judged only when it uses
// the platform secrets model: a store-backed ExternalSecret, forge.OpenBao, or
// a forge.ExternalSecretsOperator. A project that provisions its Secrets out of
// band, with no store anywhere, has nothing here to gate, and a rule that fired
// on it would be a new failure for a project that did nothing wrong.
//
// WHAT THE SCHEMA ALREADY ENFORCES IS NOT REPEATED HERE (kcl/schema.k): an
// ExternalSecret's `keys` equal what its source produces; once any ExternalSecret
// is store-backed the others need an `out_of_band_reason`; a pin above 1 on an
// `immutable` name; an OpenBaoGrant that reads a glob without a
// `wildcard_read_reason`; a plaintext listener without a reason.
//
//forge:exclude-contract: pure rules over decoded render facts; there is no collaborator to substitute
package secrets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Env is one environment's facts: its render's declarations, the manifest
// stream that render expands into, and the Secrets the stream's workloads read.
type Env struct {
	// Name is the environment (the directory under deploy/kcl).
	Name string
	// File is the project-relative path of the env's main.k: where a finding
	// with no better location is anchored.
	File string
	// Render is the declarations the env's render recorded.
	Render Render
	// Docs is the expanded manifest stream, one decoded object per document.
	Docs []Doc
	// Demands are the Secrets the stream's workloads read.
	Demands []Demand
}

// Demand is one workload's read of a Secret.
type Demand struct {
	// Workload is "Kind/name" of the object that reads it.
	Workload string
	// Namespace is where the workload resolves it ("" = the env's default).
	Namespace string
	Secret    string
	// Component says the reader is an object a forge component owns (it carries
	// the forge.dev/component-owner label), such as OpenBao's own StatefulSet.
	Component bool
	// Keys are the keys read by name; Whole says the Secret is read whole
	// (envFrom, a volume), which reads every key.
	Keys  []string
	Whole bool
}

// Render is the part of a render's `output` this lane reads.
type Render struct {
	Lifecycle     string `json:"lifecycle"`
	ClusterTarget *struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
	} `json:"cluster_target"`
	RequiredSecrets          []ExternalSecret   `json:"required_secrets"`
	ControlPlaneSecrets      []ProvidedSecret   `json:"control_plane_secrets"`
	RenderedSecrets          []RenderedSecret   `json:"rendered_secrets"`
	KubeconfigSecrets        []KubeconfigSecret `json:"kubeconfig_secrets"`
	SecretProvider           *SecretProvider    `json:"secret_provider"`
	BootstrapSource          *BootstrapSource   `json:"bootstrap_source"`
	OpenBao                  json.RawMessage    `json:"openbao"`
	ExternalSecretsOperators []json.RawMessage  `json:"external_secrets_operators"`
}

// ExternalSecret is a rendered forge.ExternalSecret.
type ExternalSecret struct {
	Name            string   `json:"name"`
	Namespace       string   `json:"namespace"`
	Keys            []string `json:"keys"`
	Reason          string   `json:"reason"`
	OutOfBandReason string   `json:"out_of_band_reason"`
	Source          *Source  `json:"source"`
}

// Source is a rendered forge.StoreSource.
type Source struct {
	Store          Store                      `json:"store"`
	Data           map[string]string          `json:"data"`
	Templates      map[string]string          `json:"templates"`
	Generate       map[string]json.RawMessage `json:"generate"`
	Pin            map[string]int             `json:"pin"`
	Immutable      []string                   `json:"immutable"`
	Refresh        string                     `json:"refresh"`
	CreationPolicy string                     `json:"creation_policy"`
	SecretType     string                     `json:"secret_type"`
	Labels         map[string]string          `json:"labels"`
	Shadow         bool                       `json:"shadow"`
	Cluster        string                     `json:"cluster"`
}

// Store is the kind of store a source reads: "hosted", "openbao" or "gcpsm".
type Store struct {
	Type    string `json:"type"`
	Project string `json:"project"`
}

// ProvidedSecret is a rendered forge.ControlPlaneSecret.
type ProvidedSecret struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Cluster   string   `json:"cluster"`
	Keys      []string `json:"keys"`
}

// RenderedSecret is a Secret forge applies from declared sources.
type RenderedSecret struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Cluster   string          `json:"cluster"`
	Keys      json.RawMessage `json:"keys"`
}

// KubeconfigSecret is a Secret forge mints from a kubeconfig.
type KubeconfigSecret struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

// SecretProvider is the env's secret provider declaration.
type SecretProvider struct {
	Type    string           `json:"type"`
	Secrets []RenderedSecret `json:"secrets"`
}

// BootstrapSource is Bundle.bootstrap_source: "gcpsm" or "out_of_band".
type BootstrapSource struct {
	Type    string `json:"type"`
	Project string `json:"project"`
}

// ParseRender decodes a render's JSON. It accepts the whole document
// (`{"output": ...}`) or the output alone.
func ParseRender(raw []byte) (Render, error) {
	var wrapper struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return Render{}, fmt.Errorf("parse render: %w", err)
	}
	body := raw
	if len(wrapper.Output) > 0 {
		body = wrapper.Output
	}
	var r Render
	if err := json.Unmarshal(body, &r); err != nil {
		return Render{}, fmt.Errorf("parse render output: %w", err)
	}
	return r, nil
}

// keyNames reads the key names of a rendered key set, written as an object
// (name -> source) or as a list of names.
func keyNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		out := make([]string, 0, len(obj))
		for k := range obj {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	return nil
}

// storeBacked reports whether any ExternalSecret reads from a store.
func (r Render) storeBacked() bool {
	for _, s := range r.RequiredSecrets {
		if s.Source != nil {
			return true
		}
	}
	return false
}

func (r Render) defaultNamespace() string {
	if r.ClusterTarget != nil {
		return r.ClusterTarget.Namespace
	}
	return ""
}

func present(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "{}" && s != "[]"
}

// Adopted reports whether the env uses the platform secrets model: a
// store-backed ExternalSecret, forge.OpenBao or forge.ExternalSecretsOperator,
// declared or rendered. Only an adopted env is judged.
func (e Env) Adopted() bool {
	r := e.Render
	if r.storeBacked() || present(r.OpenBao) || len(r.ExternalSecretsOperators) > 0 {
		return true
	}
	for _, d := range e.Docs {
		if d.group() == "forge.dev" && (d.kind() == "OpenBao" || d.kind() == "ExternalSecretsOperator") {
			return true
		}
	}
	return false
}

func (e Env) hasOpenBao() bool {
	if present(e.Render.OpenBao) {
		return true
	}
	for _, s := range e.Render.RequiredSecrets {
		if s.Source != nil && s.Source.Store.Type == "openbao" {
			return true
		}
	}
	for _, d := range e.Docs {
		if d.group() == "forge.dev" && d.kind() == "OpenBao" {
			return true
		}
	}
	return false
}

// Doc is one decoded manifest document.
type Doc map[string]any

func (d Doc) kind() string { return d.str("kind") }

func (d Doc) apiVersion() string { return d.str("apiVersion") }

// group is the API group of the document ("" for the core group).
func (d Doc) group() string {
	g, _, ok := strings.Cut(d.apiVersion(), "/")
	if !ok {
		return ""
	}
	return g
}

func (d Doc) name() string { return d.str("metadata", "name") }

func (d Doc) namespace() string { return d.str("metadata", "namespace") }

// get walks a path of map keys.
func (d Doc) get(path ...string) any {
	var cur any = map[string]any(d)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (d Doc) str(path ...string) string {
	s, _ := d.get(path...).(string)
	return s
}

func (d Doc) list(path ...string) []any {
	l, _ := d.get(path...).([]any)
	return l
}

func (d Doc) mapAt(path ...string) map[string]any {
	m, _ := d.get(path...).(map[string]any)
	return m
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
