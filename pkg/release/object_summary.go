package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Per-object descriptions a bundle carries so a release can be READ, and
// diffed against another, without its manifests.
//
// A [ShapeObject]'s Hash says THAT an object changed. What a reviewer asks
// next is WHAT changed — "image digest; env FORCE_RECONCILE removed" — and
// answering it from hashes alone would mean fetching and parsing two manifest
// layers on every page load. So the render that hashes each object also
// writes, beside the shape:
//
//   - Summary: one line saying what the object declares ("2 replicas ·
//     containers api, otel"), shown for an object a release adds or removes;
//   - Parts: one short digest per section of the object ("api.env.FOO",
//     "data.force_reconcile", "replicas"), so two releases' objects can name
//     the sections that moved ([DescribeObjectChange]).
//
// BOTH ARE VALUE-FREE. A summary names and counts (container names, key
// names, port numbers, replica counts) and a part is a digest, never the
// section's content; a Secret never reaches either because a Secret never
// rides a bundle. They live beside the shape rather than in it because a
// shape is read on every Live page and is bounded at [MaxShapeBytes]; these
// are read on a release page, under their own bound.

const (
	// MaxObjectSummaryBytes bounds one object's Summary line.
	MaxObjectSummaryBytes = 200
	// MaxObjectParts bounds one object's Parts. Past it the remainder folds
	// into one [PartOverflow] digest.
	MaxObjectParts = 64
	// MaxPartNameBytes bounds a part name.
	MaxPartNameBytes = 200
	// PartDigestLen is the hex length of a part digest: 48 bits, enough that
	// two different sections of one object never read as the same, and short
	// enough to keep a bundle's summaries small. A part digest names WHICH
	// section changed; the object Hash remains the authority on whether
	// anything did.
	PartDigestLen = 12
	// MaxObjectSummariesBytes bounds the JSON of a bundle's whole summary
	// set. [BoundObjectSummaries] drops Parts, then everything, to fit.
	MaxObjectSummariesBytes = 1 << 20
	// PartOverflow is the part that folds whatever exceeds MaxObjectParts.
	PartOverflow = "+more"
)

// ObjectSummary describes one [ShapeObject] of the same bundle, joined to it
// by Key.
type ObjectSummary struct {
	Key ObjectKey `json:"key"`
	// Summary is one value-free line saying what the object declares; ""
	// when there is nothing worth saying beyond its kind and name.
	Summary string `json:"summary,omitempty"`
	// Parts maps a section name to a [PartDigestLen]-hex digest of that
	// section's canonical JSON. Images are excluded: [ShapeObject.Images]
	// already says which artifact digest an object runs.
	Parts map[string]string `json:"parts,omitempty"`
}

var (
	partNamePattern   = regexp.MustCompile(`^[A-Za-z0-9._/\-+:\[\]@~]+$`)
	partDigestPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
	// valueLikePartNames are refused as part names: a reader that guards a
	// document against value-bearing keys must never trip over a digest.
	valueLikePartNames = map[string]bool{"value": true, "data": true, "stringData": true}
)

// Validate checks one summary: a bounded single line, and parts whose names
// are plain identifiers and whose values are digests and nothing else.
func (s ObjectSummary) Validate() error {
	if s.Key.Kind == "" || s.Key.Name == "" {
		return fmt.Errorf("%w: object summary for %s has no kind or name", ErrInvalid, s.Key)
	}
	if err := validateCommitText("object summary of "+s.Key.String(), s.Summary, MaxObjectSummaryBytes); err != nil {
		return err
	}
	if len(s.Parts) > MaxObjectParts {
		return fmt.Errorf("%w: object summary of %s has %d parts, over %d", ErrInvalid, s.Key, len(s.Parts), MaxObjectParts)
	}
	for name, digest := range s.Parts {
		if len(name) > MaxPartNameBytes || !partNamePattern.MatchString(name) || valueLikePartNames[name] {
			return fmt.Errorf("%w: object summary of %s: %q is not a part name", ErrInvalid, s.Key, name)
		}
		if !partDigestPattern.MatchString(digest) {
			return fmt.Errorf("%w: object summary of %s: part %q digest %q is not %d hex", ErrInvalid, s.Key, name, digest, PartDigestLen)
		}
	}
	return nil
}

// ValidateObjectSummaries checks a bundle's summary set against its shape:
// every summary valid, none duplicated, and each describing an object the
// shape holds.
func ValidateObjectSummaries(shape Shape, summaries []ObjectSummary) error {
	objects := make(map[ObjectKey]bool, len(shape.Objects))
	for _, o := range shape.Objects {
		objects[o.Key()] = true
	}
	seen := make(map[ObjectKey]bool, len(summaries))
	for _, s := range summaries {
		if err := s.Validate(); err != nil {
			return err
		}
		if seen[s.Key] {
			return fmt.Errorf("%w: object %s is summarized twice", ErrInvalid, s.Key)
		}
		seen[s.Key] = true
		if !objects[s.Key] {
			return fmt.Errorf("%w: object summary for %s names no object of the shape", ErrInvalid, s.Key)
		}
	}
	if b, err := json.Marshal(summaries); err != nil {
		return fmt.Errorf("%w: object summaries: %v", ErrInvalid, err)
	} else if len(b) > MaxObjectSummariesBytes {
		return fmt.Errorf("%w: object summaries are %d bytes, over the %d-byte bound", ErrInvalid, len(b), MaxObjectSummariesBytes)
	}
	return nil
}

// BoundObjectSummaries fits summaries under [MaxObjectSummariesBytes]: as
// given when they fit, without Parts when only that fits, and nil otherwise.
// A reader treats a missing summary as "not recorded", so shedding detail is
// honest where refusing the bundle would block a deploy over a description.
func BoundObjectSummaries(summaries []ObjectSummary) []ObjectSummary {
	if fits(summaries) {
		return summaries
	}
	lean := make([]ObjectSummary, len(summaries))
	for i, s := range summaries {
		lean[i] = ObjectSummary{Key: s.Key, Summary: s.Summary}
	}
	if fits(lean) {
		return lean
	}
	return nil
}

func fits(summaries []ObjectSummary) bool {
	b, err := json.Marshal(summaries)
	return err == nil && len(b) <= MaxObjectSummariesBytes
}

// ─── Summarizing a document ──────────────────────────────────────────────────

// podTemplateKinds are the kinds whose spec.template is a pod template.
var podTemplateKinds = map[string]bool{
	"Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true, "Job": true,
}

// SummarizeObject describes one rendered document (decoded YAML, secrets
// already redacted) of the given kind: its [ObjectSummary.Summary] line and
// its [ObjectSummary.Parts].
func SummarizeObject(kind string, body any) (summary string, parts map[string]string) {
	doc, ok := jsonable(body).(map[string]any)
	if !ok {
		return "", nil
	}
	p := partSet{}
	meta := asMap(doc["metadata"])
	p.add("metadata.labels", meta["labels"])
	p.add("metadata.annotations", meta["annotations"])

	var line string
	switch {
	case podTemplateKinds[kind] || kind == "CronJob" || kind == "Pod":
		line = summarizeWorkload(kind, doc, p)
	case kind == "ConfigMap":
		keys := append(p.addEach("data", asMap(doc["data"])), p.addEach("binaryData", asMap(doc["binaryData"]))...)
		p.add("immutable", doc["immutable"])
		sort.Strings(keys)
		line = listPhrase("keys", keys)
	default:
		line = summarizeOther(kind, doc)
		for _, k := range sortedMapKeys(doc) {
			switch k {
			case "apiVersion", "kind", "metadata", "status":
				continue
			}
			if m, isMap := doc[k].(map[string]any); isMap {
				p.addEach(k, m)
			} else {
				p.add(k, doc[k])
			}
		}
	}
	return oneLine(line, MaxObjectSummaryBytes), p.bounded()
}

// summarizeWorkload covers the pod-bearing kinds: the workload's own spec, its
// pod template, and each container section by section.
func summarizeWorkload(kind string, doc map[string]any, p partSet) string {
	spec := asMap(doc["spec"])
	var pod map[string]any
	var phrases []string
	switch kind {
	case "Pod":
		pod = spec
	case "CronJob":
		job := asMap(asMap(spec["jobTemplate"])["spec"])
		tmpl := asMap(job["template"])
		pod = asMap(tmpl["spec"])
		addTemplateMeta(p, tmpl)
		for _, k := range sortedMapKeys(spec) {
			switch k {
			case "jobTemplate":
			case "schedule":
				p.add("schedule", spec[k])
			default:
				p.add("spec."+k, spec[k])
			}
		}
		for _, k := range sortedMapKeys(job) {
			if k != "template" {
				p.add("job."+k, job[k])
			}
		}
		if s, ok := spec["schedule"].(string); ok && s != "" {
			phrases = append(phrases, "schedule "+s)
		}
	default:
		tmpl := asMap(spec["template"])
		pod = asMap(tmpl["spec"])
		addTemplateMeta(p, tmpl)
		for _, k := range sortedMapKeys(spec) {
			switch k {
			case "template":
			case "replicas":
				p.add("replicas", spec[k])
			default:
				p.add("spec."+k, spec[k])
			}
		}
		if n, ok := number(spec["replicas"]); ok {
			phrases = append(phrases, plural(n, "replica"))
		}
	}
	for _, k := range sortedMapKeys(pod) {
		switch k {
		case "containers", "initContainers":
		case "volumes":
			p.add("pod.volumes", pod[k])
		default:
			p.add("pod."+k, pod[k])
		}
	}
	containers := addContainers(p, pod["containers"])
	inits := addContainers(p, pod["initContainers"])
	if len(containers) > 0 {
		phrases = append(phrases, listPhrase("containers", containers))
	}
	if len(inits) > 0 {
		phrases = append(phrases, listPhrase("init", inits))
	}
	return strings.Join(phrases, " · ")
}

func addTemplateMeta(p partSet, tmpl map[string]any) {
	meta := asMap(tmpl["metadata"])
	p.add("pod.labels", meta["labels"])
	p.add("pod.annotations", meta["annotations"])
}

// containerSections are the container fields that get a part of their own;
// everything else but name and image lands in "<container>.other".
var containerSections = map[string]string{
	"command": "command", "args": "command",
	"resources": "resources", "ports": "ports",
	"livenessProbe": "probes", "readinessProbe": "probes", "startupProbe": "probes",
	"volumeMounts": "volumeMounts", "envFrom": "envFrom", "securityContext": "securityContext",
}

// addContainers records each container's sections and returns their names.
// Env is recorded per variable, because "env FOO removed" is the change a
// reviewer most often needs named.
func addContainers(p partSet, list any) []string {
	items, _ := list.([]any)
	var names []string
	for _, item := range items {
		c := asMap(item)
		name, _ := c["name"].(string)
		if name == "" {
			continue
		}
		names = append(names, name)
		grouped := map[string]map[string]any{}
		other := map[string]any{}
		for k, v := range c {
			switch k {
			case "name", "image":
				continue
			case "env":
				vars, _ := v.([]any)
				for _, ev := range vars {
					if n, _ := asMap(ev)["name"].(string); n != "" {
						p.add(name+".env."+n, ev)
					}
				}
				continue
			}
			if section, ok := containerSections[k]; ok {
				if grouped[section] == nil {
					grouped[section] = map[string]any{}
				}
				grouped[section][k] = v
				continue
			}
			other[k] = v
		}
		for section, v := range grouped {
			p.add(name+"."+section, v)
		}
		if len(other) > 0 {
			p.add(name+".other", other)
		}
	}
	return names
}

// summarizeOther is the one line for a non-workload kind.
func summarizeOther(kind string, doc map[string]any) string {
	spec := asMap(doc["spec"])
	switch kind {
	case "Service":
		typ, _ := spec["type"].(string)
		if typ == "" {
			typ = "ClusterIP"
		}
		var ports []string
		for _, item := range asList(spec["ports"]) {
			if n, ok := number(asMap(item)["port"]); ok {
				ports = append(ports, strconv.Itoa(n))
			}
		}
		if len(ports) == 0 {
			return typ
		}
		return typ + " · " + listPhrase("ports", ports)
	case "Ingress":
		var hosts []string
		for _, item := range asList(spec["rules"]) {
			if h, _ := asMap(item)["host"].(string); h != "" {
				hosts = append(hosts, h)
			}
		}
		return listPhrase("hosts", hosts)
	case "HTTPRoute", "GRPCRoute":
		return listPhrase("hostnames", stringList(spec["hostnames"]))
	case "PrometheusRule":
		var alerts []string
		records := 0
		for _, g := range asList(spec["groups"]) {
			for _, r := range asList(asMap(g)["rules"]) {
				if a, _ := asMap(r)["alert"].(string); a != "" {
					alerts = append(alerts, a)
				} else {
					records++
				}
			}
		}
		line := listPhrase("alerts", alerts)
		if records > 0 {
			line = joinNonEmpty(line, plural(records, "recording rule"))
		}
		return line
	case "CustomResourceDefinition":
		group, _ := spec["group"].(string)
		k, _ := asMap(spec["names"])["kind"].(string)
		return joinNonEmpty(group, k)
	case "Role", "ClusterRole":
		if n := len(asList(doc["rules"])); n > 0 {
			return plural(n, "rule")
		}
		return ""
	case "RoleBinding", "ClusterRoleBinding":
		role, _ := asMap(doc["roleRef"])["name"].(string)
		line := ""
		if role != "" {
			line = "role " + role
		}
		if n := len(asList(doc["subjects"])); n > 0 {
			line = joinNonEmpty(line, plural(n, "subject"))
		}
		return line
	}
	if len(spec) > 0 {
		return listPhrase("spec", sortedMapKeys(spec))
	}
	return ""
}

// ─── Parts ───────────────────────────────────────────────────────────────────

// partSet accumulates an object's parts.
type partSet map[string]string

// add records name's digest, skipping an absent section (nil, empty map or
// list) so that adding one does not read as the object's other parts moving.
func (p partSet) add(name string, v any) {
	if isEmpty(v) || name == "" {
		return
	}
	if valueLikePartNames[name] {
		// A top-level `data` that is not a map: still a section, under a
		// name no value guard mistakes for a value-bearing key.
		name += "[]"
	}
	if len(name) > MaxPartNameBytes || !partNamePattern.MatchString(name) {
		name = "~" + partDigest(name)
	}
	p[name] = partDigest(v)
}

// addEach records one part per key of m, under "<prefix>.<key>", and returns
// the keys.
func (p partSet) addEach(prefix string, m map[string]any) []string {
	keys := sortedMapKeys(m)
	for _, k := range keys {
		p.add(prefix+"."+k, m[k])
	}
	return keys
}

// bounded returns the parts, folding everything past MaxObjectParts into
// PartOverflow; nil when there are none.
func (p partSet) bounded() map[string]string {
	if len(p) == 0 {
		return nil
	}
	if len(p) <= MaxObjectParts {
		return map[string]string(p)
	}
	names := sortedMapKeys(p)
	out := make(map[string]string, MaxObjectParts)
	rest := map[string]string{}
	for i, n := range names {
		if i < MaxObjectParts-1 {
			out[n] = p[n]
		} else {
			rest[n] = p[n]
		}
	}
	out[PartOverflow] = partDigest(rest)
	return out
}

// partDigest is the first PartDigestLen hex of sha256 over v's canonical JSON.
func partDigest(v any) string {
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		b = []byte(fmt.Sprint(v))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:PartDigestLen]
}

// ─── Small helpers ───────────────────────────────────────────────────────────

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func stringList(v any) []string {
	var out []string
	for _, item := range asList(v) {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

func number(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// listPhrase is "label a, b, c", naming at most six and counting the rest.
func listPhrase(label string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	const shown = 6
	if len(names) <= shown {
		return label + " " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s %s, +%d more", label, strings.Join(names[:shown], ", "), len(names)-shown)
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, s := range parts {
		if s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, " · ")
}
