package secrets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Rule IDs. Every one is suppressible with the shared directives
// (`forge:lint-disable-next-line <rule>: <why>`) and dialable in forge.yaml
// lint.rules.
const (
	RuleClusterKind     = "secrets-cluster-kind"
	RuleESPolicy        = "secrets-es-policy"
	RuleKeyCoverage     = "secrets-key-coverage"
	RuleUnreferenced    = "secrets-unreferenced-key"
	RuleImmutablePin    = "secrets-immutable-pin"
	RuleOutOfBandReason = "secrets-out-of-band-reason"
	RuleWildcardRead    = "openbao-wildcard-read"
	RuleEscrow          = "secrets-escrow-in-cluster"
	RuleSingleOwner     = "secrets-single-owner"
)

// Rules lists every rule the lane runs, for help and tests.
var Rules = []string{
	RuleClusterKind, RuleESPolicy, RuleKeyCoverage, RuleUnreferenced, RuleImmutablePin,
	RuleOutOfBandReason, RuleWildcardRead, RuleEscrow, RuleSingleOwner,
}

// Check judges one env. An env that does not use the platform secrets model
// has nothing to judge and yields nothing.
func Check(env Env, loc *Locator) []finding.Finding {
	if !env.Adopted() {
		return nil
	}
	c := checker{env: env, loc: loc}
	c.clusterKinds()
	c.esPolicy()
	c.keyCoverage()
	c.immutablePins()
	c.outOfBandReasons()
	c.wildcardReads()
	c.escrow()
	c.singleOwner()
	sort.SliceStable(c.out, func(i, j int) bool {
		a, b := c.out[i], c.out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	return c.out
}

type checker struct {
	env Env
	loc *Locator
	out []finding.Finding
}

// add records a finding at the first place the needles match, or at the env's
// main.k. fix is the remedy; the escape is appended to it, so no finding
// leaves the lane without one.
func (c *checker) add(sev finding.Severity, rule, msg, fix string, needles ...needle) {
	file, line := c.loc.find(c.env, needles...)
	c.out = append(c.out, finding.Finding{
		Rule:     rule,
		Severity: sev,
		File:     file,
		Line:     line,
		Message:  fmt.Sprintf("env %s: %s", c.env.Name, msg),
		Remediation: fmt.Sprintf("%s Escape: a deliberate exception goes in the KCL as `# forge:lint-disable-next-line %s: <why>` above %s:%d (or `# forge:lint-disable-file %s: <why>`), or set lint.rules.%s to warn in forge.yaml.",
			fix, rule, file, line, rule, rule),
	})
}

func (c *checker) ns(n string) string {
	if n == "" {
		return c.env.Render.defaultNamespace()
	}
	return n
}

// --- secrets-cluster-kind ---------------------------------------------------

// The kinds ESO would use to reach across namespaces or write to a store. The
// component runs with their processing off and their CRDs absent, so an object
// of one is inert at best and a cluster-wide credential path at worst.
var forbiddenKinds = map[string]string{
	"ClusterSecretStore":    "reads a store with one credential for every namespace",
	"ClusterExternalSecret": "stamps ExternalSecrets into every namespace it selects",
	"PushSecret":            "writes cluster Secrets out to a store",
	"ClusterPushSecret":     "writes cluster Secrets out to a store, from every namespace it selects",
}

const esoGroup = "external-secrets.io"

func (c *checker) clusterKinds() {
	for _, d := range c.env.Docs {
		kind, why, ok := d.kind(), "", false
		switch {
		case d.group() == esoGroup:
			why, ok = forbiddenKinds[kind]
		case d.group() == "generators."+esoGroup:
			why, ok = "runs a generator that mints values inside the cluster, and the component installs no generator CRDs", true
		case kind == "CustomResourceDefinition" && d.str("spec", "group") == esoGroup:
			kind = d.str("spec", "names", "kind")
			why, ok = forbiddenKinds[kind]
			if ok {
				why += ", and its CRD must not be installed"
			}
		case kind == "CustomResourceDefinition" && d.str("spec", "group") == "generators."+esoGroup:
			kind = d.str("spec", "names", "kind")
			why, ok = "is an ESO generator, and the component installs no generator CRDs", true
		}
		if !ok {
			continue
		}
		c.add(finding.SeverityError, RuleClusterKind,
			fmt.Sprintf("%s %s is rendered, and a %s %s. The External Secrets Operator forge installs processes namespaced SecretStores and ExternalSecrets only (its cluster kinds, PushSecret and generators are disabled, their CRDs absent).",
				kind, qualified(c.ns(d.namespace()), d.name()), kind, why),
			"Use a namespaced SecretStore and ExternalSecret (a forge.ExternalSecret with a `source` renders them), and drop this object.",
			nameDecl(d.name(), kind), kindDecl(kind), word(kind))
	}
}

func qualified(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

func kindDecl(kind string) needle {
	return needle{re: regexp.MustCompile(`\bkind\s*=\s*"` + regexp.QuoteMeta(kind) + `"`)}
}

// --- secrets-es-policy ------------------------------------------------------

// minRefresh is the fastest an ExternalSecret may poll its store: a faster loop
// is a load on the store, and the cluster's admission policy refuses it.
const minRefresh = 5 * time.Minute

func (c *checker) esPolicy() {
	for _, d := range c.env.Docs {
		if d.group() != "external-secrets.io" || d.kind() != "ExternalSecret" {
			continue
		}
		who := fmt.Sprintf("ExternalSecret %s/%s", c.ns(d.namespace()), d.name())
		needles := []needle{nameDecl(d.name(), "ExternalSecret"), quoted(d.name())}
		var problems []string

		creation := d.str("spec", "target", "creationPolicy")
		switch creation {
		case "Orphan", "Merge":
		case "":
			problems = append(problems, "target.creationPolicy is unset, and ESO's default is Owner, which deletes the Secret with the ExternalSecret (a Flux prune would take a live credential with it)")
		default:
			problems = append(problems, fmt.Sprintf("target.creationPolicy is %q: only Orphan or Merge keep the Secret when the ExternalSecret goes", creation))
		}
		if dp := d.str("spec", "target", "deletionPolicy"); dp != "" && dp != "Retain" {
			problems = append(problems, fmt.Sprintf("target.deletionPolicy is %q: a store outage or a deleted store entry would delete the Secret; it must be Retain", dp))
		}
		if k := d.str("spec", "secretStoreRef", "kind"); k != "" && k != "SecretStore" {
			problems = append(problems, fmt.Sprintf("secretStoreRef.kind is %q: only a namespaced SecretStore is allowed", k))
		}
		if len(d.list("spec", "dataFrom")) > 0 {
			problems = append(problems, "spec.dataFrom pulls every key a store path holds, so a value added to the store reaches the cluster unreviewed; list the keys in spec.data")
		}
		if t := d.str("spec", "target", "name"); t != "" && t != d.name() {
			problems = append(problems, fmt.Sprintf("target.name %q differs from the ExternalSecret's own name: it would write a Secret another declaration owns", t))
		}
		if ri := d.str("spec", "refreshInterval"); ri != "" {
			if dur, err := time.ParseDuration(ri); err == nil && dur > 0 && dur < minRefresh {
				problems = append(problems, fmt.Sprintf("refreshInterval %s is under %s", ri, minRefresh))
			}
		}
		for _, p := range problems {
			c.add(finding.SeverityError, RuleESPolicy, who+": "+p+".",
				"Make the ExternalSecret an Orphan or Merge, Retain, namespaced-store, explicit-key declaration; a forge.ExternalSecret with a `source` renders exactly that.",
				needles...)
		}
	}
}

// --- supply and demand ------------------------------------------------------

type supply struct {
	kind string
	ns   string
	name string
	// keys is the set of data keys the owner produces; nil means unknown, and
	// any key is taken as covered.
	keys map[string]bool
}

func keySet(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func (c *checker) supplies() []supply {
	r := c.env.Render
	var out []supply
	for _, s := range r.RequiredSecrets {
		kind := "ExternalSecret (out of band)"
		if s.Source != nil {
			kind = "ExternalSecret (" + s.Source.Store.Type + " store)"
		}
		out = append(out, supply{kind: kind, ns: c.ns(s.Namespace), name: s.Name, keys: keySet(s.Keys...)})
	}
	for _, s := range r.ControlPlaneSecrets {
		out = append(out, supply{kind: "ControlPlaneSecret", ns: c.ns(s.Namespace), name: s.Name, keys: keySet(s.Keys...)})
	}
	for _, s := range r.RenderedSecrets {
		out = append(out, supply{kind: "rendered_secrets", ns: c.ns(s.Namespace), name: s.Name, keys: keySetOrNil(keyNames(s.Keys))})
	}
	if p := r.SecretProvider; p != nil && p.Type == "rendered" {
		for _, s := range p.Secrets {
			out = append(out, supply{kind: "RenderedSecrets provider", ns: c.ns(s.Namespace), name: s.Name, keys: keySetOrNil(keyNames(s.Keys))})
		}
	}
	for _, k := range r.KubeconfigSecrets {
		out = append(out, supply{kind: "KubeconfigSecret", ns: c.ns(k.Namespace), name: k.Name, keys: nil})
	}
	forgeES := c.forgeRenderedESTargets()
	for _, d := range c.env.Docs {
		switch {
		case d.group() == "" && d.kind() == "Secret":
			out = append(out, supply{kind: "rendered Secret", ns: c.ns(d.namespace()), name: d.name(), keys: docSecretKeys(d)})
		case d.group() == "cert-manager.io" && d.kind() == "Certificate":
			if n := d.str("spec", "secretName"); n != "" {
				out = append(out, supply{kind: "cert-manager Certificate", ns: c.ns(d.namespace()), name: n, keys: nil})
			}
		case d.group() == "postgresql.cnpg.io" && d.kind() == "Cluster":
			for _, suffix := range []string{"app", "superuser", "ca", "server", "replication"} {
				out = append(out, supply{kind: "CloudNativePG cluster", ns: c.ns(d.namespace()), name: d.name() + "-" + suffix, keys: nil})
			}
		case d.group() == "external-secrets.io" && d.kind() == "ExternalSecret":
			target := d.str("spec", "target", "name")
			if target == "" {
				target = d.name()
			}
			if forgeES[c.ns(d.namespace())+"/"+d.name()] && target == d.name() {
				continue
			}
			out = append(out, supply{kind: "ExternalSecret object", ns: c.ns(d.namespace()), name: target, keys: esDocKeys(d)})
		}
	}
	return out
}

func keySetOrNil(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	return keySet(names...)
}

// forgeRenderedESTargets are the (namespace/object name) pairs of the
// ExternalSecret objects forge renders for the declared required_secrets: the
// same declaration, so not a second owner. A hand-written object is told apart
// by its name, or by a target that is not its own name.
func (c *checker) forgeRenderedESTargets() map[string]bool {
	m := map[string]bool{}
	for _, s := range c.env.Render.RequiredSecrets {
		if s.Source == nil {
			continue
		}
		m[c.ns(s.Namespace)+"/"+s.Name] = true
		m[c.ns(s.Namespace)+"/"+s.Name+"-eso-shadow"] = true
	}
	return m
}

// docSecretKeys reads a Secret object's keys; nil (any) when it carries none
// (its data is written by something else, or by a field this does not read).
func docSecretKeys(d Doc) map[string]bool {
	var names []string
	for _, f := range []string{"data", "stringData"} {
		for k := range d.mapAt(f) {
			names = append(names, k)
		}
	}
	return keySetOrNil(names)
}

// esDocKeys reads the keys an ExternalSecret object produces; nil (any) when it
// pulls whole paths or templates from other sources.
func esDocKeys(d Doc) map[string]bool {
	if len(d.list("spec", "dataFrom")) > 0 || len(d.list("spec", "target", "template", "templateFrom")) > 0 {
		return nil
	}
	names := []string{}
	for _, it := range d.list("spec", "data") {
		if k := asString(asMap(it)["secretKey"]); k != "" {
			names = append(names, k)
		}
	}
	for k := range d.mapAt("spec", "target", "template", "data") {
		names = append(names, k)
	}
	return keySet(names...)
}

func sameNS(a, b string) bool { return a == "" || b == "" || a == b }

// --- secrets-key-coverage ---------------------------------------------------

func (c *checker) keyCoverage() {
	sups := c.supplies()
	// A file secret provider mints a Secret for every secret_ref at deploy
	// (local lifecycles only): its supply is not in the render.
	if p := c.env.Render.SecretProvider; p != nil && p.Type == "file" {
		return
	}
	// A forge component's own pods read Secrets that are the component's
	// contract. Under a GCP bootstrap_source the bootstrap component delivers
	// them and they are declared nowhere in the env, so they are not judged;
	// under an out-of-band source the component declares them as prerequisites,
	// and they are judged like any other.
	bootstrapDelivers := c.env.Render.BootstrapSource != nil && c.env.Render.BootstrapSource.Type == "gcpsm"
	for _, d := range c.env.Demands {
		if d.Component && bootstrapDelivers {
			continue
		}
		d.Namespace = c.ns(d.Namespace)
		var matching []supply
		for _, s := range sups {
			if s.name == d.Secret && sameNS(s.ns, d.Namespace) {
				matching = append(matching, s)
			}
		}
		if len(matching) == 0 {
			elsewhere := ""
			for _, s := range sups {
				if s.name == d.Secret {
					elsewhere = fmt.Sprintf(" (a %s named %s exists in namespace %s, not %s)", s.kind, s.name, s.ns, d.Namespace)
					break
				}
			}
			c.add(finding.SeverityError, RuleKeyCoverage,
				fmt.Sprintf("%s reads Secret %s/%s, and no ExternalSecret, ControlPlaneSecret, rendered Secret or generated Secret produces it%s. The pod would sit in CreateContainerConfigError.", d.Workload, d.Namespace, d.Secret, elsewhere),
				"Declare it: a forge.ExternalSecret with a `source` (store-backed), or without one and with `out_of_band_reason` saying who provisions it; or remove the reference.",
				nameDecl(d.Secret, "ExternalSecret"), quoted(d.Secret))
			continue
		}
		if d.Whole {
			continue
		}
		union, anyUnknown := map[string]bool{}, false
		for _, s := range matching {
			if s.keys == nil {
				anyUnknown = true
			}
			for kk := range s.keys {
				union[kk] = true
			}
		}
		if anyUnknown {
			continue
		}
		var missing []string
		for _, kk := range d.Keys {
			if !union[kk] {
				missing = append(missing, kk)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			c.add(finding.SeverityError, RuleKeyCoverage,
				fmt.Sprintf("%s reads key %s of Secret %s/%s, and the declaration that produces it (%s) does not list it. With creationPolicy Orphan ESO rebuilds the Secret from the declared keys only, so the key is dropped on the next sync; with Merge it exists only while something else keeps writing it.",
					d.Workload, strings.Join(missing, ", "), d.Namespace, d.Secret, matching[0].kind),
				"Add the key to the ExternalSecret's `keys` and to its source's `data` (or `templates`), or stop reading it.",
				nameDecl(d.Secret, "ExternalSecret"), quoted(d.Secret))
		}
	}
	c.unreferenced()
}

// unreferenced is the P10 tool: a declared Secret, or a declared key, that no
// workload of this env reads. Advisory, because a controller, a Job forge
// applies at deploy, or another env may still read it.
func (c *checker) unreferenced() {
	type use struct {
		whole bool
		keys  map[string]bool
	}
	uses := map[string]*use{}
	for _, d := range c.env.Demands {
		u := uses[d.Secret]
		if u == nil {
			u = &use{keys: map[string]bool{}}
			uses[d.Secret] = u
		}
		u.whole = u.whole || d.Whole
		for _, k := range d.Keys {
			u.keys[k] = true
		}
	}
	for _, s := range c.env.Render.RequiredSecrets {
		if s.Namespace == "" {
			continue
		}
		u := uses[s.Name]
		switch {
		case u == nil:
			c.add(finding.SeverityInfo, RuleUnreferenced,
				fmt.Sprintf("ExternalSecret %s/%s is declared, and no workload this env renders reads it. If a controller, a Job or another env reads it, ignore this; otherwise it is an orphan.", s.Namespace, s.Name),
				"Remove the declaration if nothing reads the Secret, or say what does in its `reason`.",
				nameDecl(s.Name, "ExternalSecret"), quoted(s.Name))
		case !u.whole:
			var unread []string
			for _, k := range s.Keys {
				if !u.keys[k] {
					unread = append(unread, k)
				}
			}
			if len(unread) > 0 {
				c.add(finding.SeverityInfo, RuleUnreferenced,
					fmt.Sprintf("ExternalSecret %s/%s declares key(s) %s, and no workload this env renders reads them. If something else reads them, ignore this; otherwise they are orphans.", s.Namespace, s.Name, strings.Join(unread, ", ")),
					"Drop the keys from the declaration (and from the store) if nothing reads them.",
					nameDecl(s.Name, "ExternalSecret"), quoted(s.Name))
			}
		}
	}
}

// --- secrets-immutable-pin --------------------------------------------------

// A pinned immutable name cannot float. The schema refuses an `immutable` name
// pinned above 1; what it does not see is the name left UNPINNED (a floating
// read follows the latest version, so rotating the value in the store changes
// what the pod gets, destroying whatever the value protects), or read by
// another source that does not pin it.
func (c *checker) immutablePins() {
	type ref struct {
		who    string
		pinned bool
		ver    int
	}
	type storeName struct{ store, project, name string }
	immutable := map[storeName]bool{}
	reads := map[storeName][]ref{}
	for _, s := range c.env.Render.RequiredSecrets {
		src := s.Source
		if src == nil {
			continue
		}
		imm := map[string]bool{}
		for _, n := range src.Immutable {
			imm[n] = true
		}
		names := map[string]bool{}
		for _, n := range src.Data {
			names[n] = true
		}
		for n := range names {
			sn := storeName{src.Store.Type, src.Store.Project, n}
			v, pinned := src.Pin[n]
			reads[sn] = append(reads[sn], ref{who: s.Namespace + "/" + s.Name, pinned: pinned, ver: v})
			if imm[n] {
				immutable[sn] = true
			}
		}
	}
	var sns []storeName
	for sn := range immutable {
		sns = append(sns, sn)
	}
	sort.Slice(sns, func(i, j int) bool { return sns[i].name < sns[j].name })
	for _, sn := range sns {
		for _, r := range reads[sn] {
			switch {
			case !r.pinned:
				c.add(finding.SeverityError, RuleImmutablePin,
					fmt.Sprintf("store name %s is immutable (it is never rotated in place), and ExternalSecret %s reads it without a pin. A floating read follows the store's latest version, so a write to the store changes the value the pod gets.", sn.name, r.who),
					fmt.Sprintf("Pin it: `pin = {%q = 1}` in the source (and list it in `immutable` there); a new value is a NEW name, migrated by a reviewed change.", sn.name),
					nameDecl(strings.SplitN(r.who, "/", 2)[1], "ExternalSecret"), quoted(sn.name))
			case r.ver != 1:
				c.add(finding.SeverityError, RuleImmutablePin,
					fmt.Sprintf("store name %s is immutable, and ExternalSecret %s pins version %d: an immutable name has exactly one version.", sn.name, r.who, r.ver),
					"Pin version 1, or mint a new store name for the new value.",
					nameDecl(strings.SplitN(r.who, "/", 2)[1], "ExternalSecret"), quoted(sn.name))
			}
		}
	}
}

// --- secrets-out-of-band-reason ---------------------------------------------

// The schema requires an out_of_band_reason once any ExternalSecret is
// store-backed. It cannot tell a reason from "todo", so this does: a reason is
// the answer to "why is this Secret not synced from the store", and a
// restatement of the fact ("manually provisioned") or a placeholder is not one.
func (c *checker) outOfBandReasons() {
	for _, s := range c.env.Render.RequiredSecrets {
		if s.Source != nil || strings.TrimSpace(s.OutOfBandReason) == "" {
			continue
		}
		if why := weakReason(s.OutOfBandReason, s.Name); why != "" {
			c.add(finding.SeverityError, RuleOutOfBandReason,
				fmt.Sprintf("ExternalSecret %s/%s gives out_of_band_reason %q, which %s.", s.Namespace, s.Name, strings.TrimSpace(s.OutOfBandReason), why),
				"Say why this Secret is not synced from the store, and what will change that (e.g. \"cert-manager DNS-01 token: read before the store is up; moves to the store in wave W5\").",
				nameDecl(s.Name, "ExternalSecret"), field("out_of_band_reason"), quoted(s.Name))
		}
	}
}

// reasonStop are words that restate a Secret is provisioned by hand, or hedge,
// without saying why. A reason must carry at least two words that are not these.
var reasonStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`out of band oob manual manually provision provisioned provisioning created create by hand
		secret secrets the a an is are was be it this that to and or in via with from kubectl script scripts externally external
		other otherwise not yet store backed managed todo tbd n na none wip fixme temporary tmp temp legacy later for now as on
		set up setup done will should needs need needed`) {
		m[w] = true
	}
	return m
}()

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// weakReason returns why a reason is not one, or "" when it is. subject names
// the thing the reason is about, so a reason that only repeats it fails.
func weakReason(reason, subject string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) < 12 {
		return "is too short to explain anything"
	}
	subj := map[string]bool{}
	for _, w := range words(subject) {
		subj[w] = true
	}
	content := 0
	for _, w := range words(reason) {
		if !reasonStop[w] && !subj[w] {
			content++
		}
	}
	if content < 2 {
		return "restates that the Secret is provisioned by hand, or is a placeholder, and does not say why"
	}
	return ""
}

// --- openbao-wildcard-read --------------------------------------------------

// accessDoc is the part of the OpenBao component's rendered access document
// (the ConfigMap's access.json) this reads.
type accessDoc struct {
	KV struct {
		Mount string `json:"mount"`
	} `json:"kv"`
	Policies []struct {
		Name  string `json:"name"`
		Rules []struct {
			Path         string   `json:"path"`
			Capabilities []string `json:"capabilities"`
			Reason       string   `json:"reason"`
		} `json:"rules"`
	} `json:"policies"`
}

func hasGlob(p string) bool { return strings.ContainsAny(p, "*+") }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// readsValues reports whether a policy path under the KV mount can expose a
// stored VALUE: data/ (KV v2), or a glob that may stand for it. metadata/ lists
// names and versions, and the other KV subtrees hold none.
func readsValues(path, mount string) bool {
	rest, ok := strings.CutPrefix(path, mount+"/")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	switch first {
	case "data":
		return true
	case "metadata", "delete", "undelete", "destroy", "config", "subkeys":
		return false
	}
	return hasGlob(first)
}

// The schema refuses a glob `read` grant with no wildcard_read_reason. This
// judges the rendered access document, where grants arrive by other routes (a
// derived binding, a policy merged by a later widening), and holds the reason
// to the same standard as any other: a stated reason that states nothing is a
// reasonless wildcard.
func (c *checker) wildcardReads() {
	for _, d := range c.env.Docs {
		if d.kind() != "ConfigMap" {
			continue
		}
		raw := asString(d.mapAt("data")["access.json"])
		if raw == "" {
			continue
		}
		var a accessDoc
		if json.Unmarshal([]byte(raw), &a) != nil || a.KV.Mount == "" {
			continue
		}
		for _, p := range a.Policies {
			for _, r := range p.Rules {
				if !hasGlob(r.Path) || !contains(r.Capabilities, "read") || !readsValues(r.Path, a.KV.Mount) {
					continue
				}
				problem := ""
				if strings.TrimSpace(r.Reason) == "" {
					problem = "states no reason"
				} else if why := weakReason(r.Reason, p.Name); why != "" {
					problem = fmt.Sprintf("states the reason %q, which %s", strings.TrimSpace(r.Reason), why)
				}
				if problem == "" {
					continue
				}
				c.add(finding.SeverityError, RuleWildcardRead,
					fmt.Sprintf("OpenBao policy %s grants read on %s, a glob under the %s/ mount, and %s. A glob read exposes every value under it to that principal.", p.Name, r.Path, a.KV.Mount, problem),
					"Grant exact paths (the names an ExternalSecret reads), or set `wildcard_read_reason` on the forge.OpenBaoGrant to say why they cannot be named and when the glob goes.",
					quoted(r.Path), field("wildcard_read_reason"), nameDecl(strings.TrimPrefix(p.Name, "consumer-"), "OpenBaoConsumer"))
			}
		}
	}
}

// --- secrets-escrow-in-cluster ----------------------------------------------

// A store whose recovery output and seal key are escrowed inside the cluster it
// runs on loses both with the cluster, and the unseal key is also what makes its
// backups readable. Only a real env is judged: a local or ephemeral cluster
// holds nothing worth escrowing.
func (c *checker) escrow() {
	r := c.env.Render
	if !c.env.hasOpenBao() || r.Lifecycle != "" {
		return
	}
	if b := r.BootstrapSource; b != nil && b.Type != "out_of_band" {
		return
	}
	c.add(finding.SeverityWarning, RuleEscrow,
		"forge.OpenBao runs here with bootstrap_source out of band (the default), so its seal key and `bao operator init` output are escrowed in the cluster they protect. Losing the cluster loses the key, and with it every backup of the store.",
		"Set `bootstrap_source = forge.GCPSecretManager {project = \"...\"}` so they are escrowed outside the cluster.",
		field("bootstrap_source"), field("openbao"), word("OpenBao"))
}

// --- secrets-single-owner ---------------------------------------------------

// A Secret has one owner. Two declarations that both produce it fight over its
// keys on every sync, and the loser's keys disappear without an error.
func (c *checker) singleOwner() {
	type id struct{ ns, name string }
	owners := map[id][]string{}
	var order []id
	add := func(ns, name, kind string) {
		k := id{c.ns(ns), name}
		if _, seen := owners[k]; !seen {
			order = append(order, k)
		}
		owners[k] = append(owners[k], kind)
	}
	for _, s := range c.env.Render.RequiredSecrets {
		kind := "forge.ExternalSecret (out of band)"
		if s.Source != nil {
			kind = "forge.ExternalSecret (" + s.Source.Store.Type + " store)"
		}
		add(s.Namespace, s.Name, kind)
	}
	for _, s := range c.env.Render.ControlPlaneSecrets {
		add(s.Namespace, s.Name, "forge.ControlPlaneSecret")
	}
	for _, s := range c.env.Render.RenderedSecrets {
		add(s.Namespace, s.Name, "rendered_secrets")
	}
	if p := c.env.Render.SecretProvider; p != nil && p.Type == "rendered" {
		for _, s := range p.Secrets {
			add(s.Namespace, s.Name, "RenderedSecrets provider")
		}
	}
	for _, k := range c.env.Render.KubeconfigSecrets {
		add(k.Namespace, k.Name, "forge.KubeconfigSecret")
	}
	forgeES := c.forgeRenderedESTargets()
	for _, d := range c.env.Docs {
		switch {
		case d.group() == "" && d.kind() == "Secret":
			add(d.namespace(), d.name(), "a Secret object in the render")
		case d.group() == "cert-manager.io" && d.kind() == "Certificate":
			if n := d.str("spec", "secretName"); n != "" {
				add(d.namespace(), n, "cert-manager Certificate "+d.name())
			}
		case d.group() == "external-secrets.io" && d.kind() == "ExternalSecret":
			target := d.str("spec", "target", "name")
			if target == "" {
				target = d.name()
			}
			if !(forgeES[c.ns(d.namespace())+"/"+d.name()] && target == d.name()) {
				add(d.namespace(), target, "an ExternalSecret object "+d.name())
			}
		}
	}
	for _, k := range order {
		if len(owners[k]) < 2 {
			continue
		}
		c.add(finding.SeverityError, RuleSingleOwner,
			fmt.Sprintf("Secret %s/%s has %d owners: %s. They fight over its keys on every sync, and the loser's keys disappear without an error.", k.ns, k.name, len(owners[k]), strings.Join(owners[k], "; ")),
			"Keep one declaration for it. To split its keys between sources, give each source its own Secret name and have the workload read both.",
			nameDecl(k.name, "ExternalSecret"), quoted(k.name))
	}
}
