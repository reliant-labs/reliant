package release

import (
	"fmt"
	"regexp"
	"strings"
)

// PlacementPrerequisitesVersion is the graph version BundleFeaturePlacementPrerequisites
// names (placement-prerequisites-v2).
const PlacementPrerequisitesVersion = 2

// PlacementPrerequisites is sealed dependency evidence, not an assertion of readiness.
// Absence is unknown; a present empty graph declares independence.
type PlacementPrerequisites struct {
	Version  int                `json:"version"`
	Items    []PlacementItem    `json:"items"`
	Edges    []PlacementEdge    `json:"edges"`
	Bindings []PlacementBinding `json:"bindings"`
}

// PlacementRuntimeKind reports whether kind runs something on a cluster. A
// mixed bundle observes each such object (Deployment, StatefulSet, DaemonSet,
// Job) or refuses it (ReplicaSet, Pod: owned by a controller in a real
// render).
func PlacementRuntimeKind(kind string) bool {
	switch kind {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Pod", "Job":
		return true
	}
	return false
}

// PlacementPassiveKind reports whether a cluster object of kind is applied
// inventory in a mixed bundle (a hosted record is always an item): it has no readiness item of its own. That is every kind
// except the runtime kinds, ConfigMap (bound to the items of its namespace)
// and Secret (never in the bundle). A Service, a Role or RoleBinding, a route or a
// policy attachment affects ordinary rollout readiness only through the
// healthy Pods of the component that uses it; a CRD, an admission policy or a
// scheduling class changes the cluster's API; a CronJob's spec is applied and
// starts nothing; a custom resource is its controller's input, and the
// controller is an item of its own. No kind is refused for being unknown.
//
// forge's producer and a control plane's reader share this rule, at the forge
// the control plane pins, so the two can never disagree about one bundle.
func PlacementPassiveKind(kind string) bool {
	return !PlacementRuntimeKind(kind) && kind != "ConfigMap" && kind != "Secret"
}

// PlacementKey identifies an actual placement independently of its logical workload.
type PlacementKey struct {
	Runtime   string `json:"runtime"`
	Actuator  string `json:"actuator"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

// PlacementItem associates an actual placement with its release component.
type PlacementItem struct {
	Key      PlacementKey `json:"key"`
	Workload string       `json:"workload"`
}

// PlacementEdge requires Predecessor readiness before Dependent activation.
type PlacementEdge struct {
	Predecessor PlacementKey `json:"predecessor"`
	Dependent   PlacementKey `json:"dependent"`
}

// PlacementObject locates an object in the verified sealed manifest inventory.
type PlacementObject struct {
	Cluster    string `json:"cluster"`
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// PlacementBinding binds an item to a same-parse ShapeObject.Hash, not a live spec hash.
// JobContract proves only ordinary Kubernetes successful completion, never SQL effects.
type PlacementBinding struct {
	Item         PlacementKey    `json:"item"`
	Object       PlacementObject `json:"object"`
	ManifestHash string          `json:"manifest_hash"`
	JobContract  string          `json:"job_contract,omitempty"`
}

// KubernetesJobCompleteV1 names ordinary completion of the exact sealed Job.
const KubernetesJobCompleteV1 = "kubernetes-job-complete-v1"

var placementName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
var placementNamespace = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
var placementKind = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

func placementToken(s string) bool {
	return s != "" && s == strings.TrimSpace(s) && !strings.ContainsAny(s, "/\\\x00\r\n\t") && s != "." && s != ".."
}

func (k PlacementKey) validate() error {
	if !((k.Runtime == "cluster" && k.Actuator == "forge") || (k.Runtime == "hosted" && k.Actuator == "platform")) {
		return fmt.Errorf("%w: unsupported placement runtime/actuator %q/%q", ErrInvalid, k.Runtime, k.Actuator)
	}
	if !placementToken(k.Cluster) || (k.Namespace == "" && k.Runtime != "hosted") || (k.Namespace != "" && !placementNamespace.MatchString(k.Namespace)) || len(k.Namespace) > 63 || !placementKind.MatchString(k.Kind) || !placementName.MatchString(k.Name) || len(k.Name) > 253 {
		return fmt.Errorf("%w: invalid placement identity %+v", ErrInvalid, k)
	}
	if (k.Runtime == "hosted" && k.Cluster != BundleHostedCluster) || (k.Runtime == "cluster" && k.Cluster == BundleHostedCluster) {
		return fmt.Errorf("%w: reserved hosted placement cluster misuse", ErrInvalid)
	}
	if k.Cluster == "_" || BundlePathSegment(k.Cluster) == "-" {
		return fmt.Errorf("%w: invalid placement cluster %q", ErrInvalid, k.Cluster)
	}
	return nil
}

// Validate checks the complete typed graph. The reader must additionally match
// every descriptor/hash and the complete inventory against admitted sealed bytes.
func (p *PlacementPrerequisites) Validate() error {
	if p == nil {
		return nil
	}
	if p.Version != PlacementPrerequisitesVersion {
		return fmt.Errorf("%w: placement prerequisites version %d", ErrInvalid, p.Version)
	}
	items := make(map[PlacementKey]bool, len(p.Items))
	identities := map[[4]string]bool{}
	aliases := map[string]string{}
	for _, item := range p.Items {
		k := item.Key
		if err := k.validate(); err != nil {
			return err
		}
		if !placementToken(item.Workload) {
			return fmt.Errorf("%w: invalid placement workload %q", ErrInvalid, item.Workload)
		}
		id := [4]string{k.Cluster, k.Namespace, k.Kind, k.Name}
		if items[k] || identities[id] {
			return fmt.Errorf("%w: duplicate/colliding placement item %+v", ErrInvalid, k)
		}
		segment := BundlePathSegment(k.Cluster)
		if prev, ok := aliases[segment]; ok && prev != k.Cluster {
			return fmt.Errorf("%w: colliding placement clusters %q and %q", ErrInvalid, prev, k.Cluster)
		}
		aliases[segment] = k.Cluster
		items[k], identities[id] = true, true
	}
	edges := map[PlacementEdge]bool{}
	after := map[PlacementKey][]PlacementKey{}
	for _, e := range p.Edges {
		if !items[e.Predecessor] || !items[e.Dependent] {
			return fmt.Errorf("%w: placement edge endpoint missing", ErrInvalid)
		}
		if e.Predecessor == e.Dependent || edges[e] {
			return fmt.Errorf("%w: self/duplicate placement edge", ErrInvalid)
		}
		edges[e] = true
		after[e.Dependent] = append(after[e.Dependent], e.Predecessor)
	}
	state := map[PlacementKey]int{}
	var visit func(PlacementKey) error
	visit = func(k PlacementKey) error {
		if state[k] == 1 {
			return fmt.Errorf("%w: placement dependency cycle at %+v", ErrInvalid, k)
		}
		if state[k] == 2 {
			return nil
		}
		state[k] = 1
		for _, peer := range after[k] {
			if err := visit(peer); err != nil {
				return err
			}
		}
		state[k] = 2
		return nil
	}
	for _, item := range p.Items {
		if err := visit(item.Key); err != nil {
			return err
		}
	}
	seen := map[struct {
		Item   PlacementKey
		Object PlacementObject
	}]bool{}
	objects := map[[4]string]PlacementObject{}
	hashes := map[PlacementObject]string{}
	for _, b := range p.Bindings {
		o := b.Object
		if !items[b.Item] {
			return fmt.Errorf("%w: placement binding item missing", ErrInvalid)
		}
		if o.Cluster != b.Item.Cluster || o.Namespace != b.Item.Namespace || !placementKind.MatchString(o.Kind) || !placementName.MatchString(o.Name) || len(o.Name) > 253 || !validPlacementAPIVersion(o.APIVersion) || !ValidDigest(b.ManifestHash) || o.Kind == "Secret" {
			return fmt.Errorf("%w: invalid placement object binding %+v", ErrInvalid, o)
		}
		if (o.Kind == "Job" && (o.APIVersion != "batch/v1" || b.JobContract != KubernetesJobCompleteV1)) || (o.Kind != "Job" && b.JobContract != "") {
			return fmt.Errorf("%w: invalid placement job contract %q", ErrInvalid, b.JobContract)
		}
		key := struct {
			Item   PlacementKey
			Object PlacementObject
		}{b.Item, o}
		id := [4]string{o.Cluster, o.Namespace, o.Kind, o.Name}
		if seen[key] {
			return fmt.Errorf("%w: duplicate placement binding", ErrInvalid)
		}
		if prev, ok := objects[id]; ok && prev != o {
			return fmt.Errorf("%w: colliding placement object", ErrInvalid)
		}
		if prev, ok := hashes[o]; ok && prev != b.ManifestHash {
			return fmt.Errorf("%w: conflicting placement binding hash", ErrInvalid)
		}
		seen[key], objects[id], hashes[o] = true, o, b.ManifestHash
	}
	return nil
}

func validPlacementAPIVersion(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !placementName.MatchString(part) {
			return false
		}
	}
	return true
}
