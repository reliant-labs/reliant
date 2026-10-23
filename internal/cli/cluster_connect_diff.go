package cli

// `--dry-run`: the exact RBAC diff between what connect WOULD apply and what is
// on the cluster now. It is what the owner reviews before widening a grant.
//
// The desired side is the same objects a real run applies (grantObjects and the
// delivery Roles), so the review and the apply cannot disagree. The live side
// is read through the owner's own context with a label selector (so it names
// only what this binding owns), or from a file (`--live-from`, a
// `kubectl get … -o yaml|json` capture) for a review with no cluster access.
//
// The diff covers the RBAC kinds. The delivery admission policy and its
// binding are listed as applied and not diffed: a live ValidatingAdmissionPolicy
// carries server-defaulted fields that would drown the review in noise.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/cluster"
)

// liveObject is one RBAC object read from the cluster (or a capture),
// already normalized to the fields forge writes.
type liveObject struct {
	Kind      string
	Namespace string
	Name      string
	Delivery  bool
	Body      map[string]any
}

var rbacKindNames = map[string]bool{"Role": true, "RoleBinding": true, "ClusterRole": true, "ClusterRoleBinding": true}

// normalizeRBAC keeps the fields forge writes and nothing the server adds, so
// a live object and its desired twin compare equal when they grant the same.
func normalizeRBAC(kind, namespace, name string, body map[string]any) map[string]any {
	out := map[string]any{"kind": kind, "name": name}
	if namespace != "" {
		out["namespace"] = namespace
	}
	for _, k := range []string{"rules", "roleRef", "subjects"} {
		if v, ok := body[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// parseLive reads kubectl output (a List, one object, or a stream of them, as
// JSON or YAML) into the binding's RBAC objects.
func parseLive(data []byte) ([]liveObject, error) {
	var out []liveObject
	var add func(map[string]any)
	add = func(obj map[string]any) {
		kind, _ := obj["kind"].(string)
		if items, ok := obj["items"].([]any); ok && strings.HasSuffix(kind, "List") {
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					add(m)
				}
			}
			return
		}
		if !rbacKindNames[kind] {
			return
		}
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		labels, _ := meta["labels"].(map[string]any)
		grant, _ := labels[deliveryGrantLabelKey].(string)
		out = append(out, liveObject{
			Kind: kind, Namespace: ns, Name: name, Delivery: grant == deliveryGrantLabelValue,
			Body: normalizeRBAC(kind, ns, name, obj),
		})
	}
	for _, doc := range strings.Split(string(data), "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, fmt.Errorf("parse live RBAC: %w", err)
		}
		if obj != nil {
			add(obj)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return objectLess(out[i].Kind, out[i].Namespace, out[i].Name, out[j].Kind, out[j].Namespace, out[j].Name)
	})
	return out, nil
}

func objectLess(ak, ans, an, bk, bns, bn string) bool {
	if ak != bk {
		return ak < bk
	}
	if ans != bns {
		return ans < bns
	}
	return an < bn
}

// bindingSelector selects every object this (cluster, env) binding owns.
func (r connectRBAC) bindingSelector() string {
	return forgeManagedLabelKey + "=" + forgeManagedLabelValue + "," +
		connectClusterLabelKey + "=" + r.ClusterName + "," +
		connectBindingLabelKey + "=" + dnsSegment(r.Env)
}

// connectReadLive is the cluster read, a seam so tests supply a cluster.
var connectReadLive = func(ctx context.Context, kctx string, r connectRBAC) ([]liveObject, error) {
	cmd := exec.CommandContext(ctx, "kubectl", cluster.KubectlArgs(kctx, "get",
		"clusterrole,clusterrolebinding,role,rolebinding", "--all-namespaces",
		"-l", r.bindingSelector(), "-o", "json")...)
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("read the binding's live RBAC through context %q: %w\n%s\n"+
			"fix: point --context at a reachable cluster, or review offline with `--live-from <file>` "+
			"(`kubectl get clusterrole,clusterrolebinding,role,rolebinding -A -l %s -o yaml > live.yaml`)",
			kctx, err, msg, r.bindingSelector())
	}
	return parseLive(out)
}

// rbacDiffEntry is one object's verdict.
type rbacDiffEntry struct {
	Verdict string // "+", "~", "-", "="
	Ref     string
	Lines   []string
}

// rbacDiff compares desired objects against live ones. Live objects of the
// delivery grant that are not desired are kept (connect never prunes them) and
// are not reported.
func rbacDiff(desired []rbacObject, live []liveObject) (entries []rbacDiffEntry, widened []string) {
	liveBy := map[string]liveObject{}
	for _, l := range live {
		liveBy[l.Kind+"\x00"+l.Namespace+"\x00"+l.Name] = l
	}
	seen := map[string]bool{}
	for _, d := range desired {
		key := d.Kind + "\x00" + d.Namespace + "\x00" + d.Name
		seen[key] = true
		want := normalizeRBAC(d.Kind, d.Namespace, d.Name, d.Body)
		l, ok := liveBy[key]
		if !ok {
			entries = append(entries, rbacDiffEntry{"+", d.ref(), prefixLines("+ ", yamlLines(want))})
			continue
		}
		wl, ll := yamlLines(want), yamlLines(l.Body)
		if strings.Join(wl, "\n") == strings.Join(ll, "\n") {
			entries = append(entries, rbacDiffEntry{"=", d.ref(), nil})
			continue
		}
		entries = append(entries, rbacDiffEntry{"~", d.ref(), lineDiff(ll, wl)})
	}
	for _, l := range live {
		key := l.Kind + "\x00" + l.Namespace + "\x00" + l.Name
		if seen[key] || l.Delivery {
			continue
		}
		ref := l.Kind + " " + l.Name
		if l.Namespace != "" {
			ref = l.Kind + " " + l.Namespace + "/" + l.Name
		}
		entries = append(entries, rbacDiffEntry{"-", ref, prefixLines("- ", yamlLines(l.Body))})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Ref < entries[j].Ref })

	// What the review is really for: permissions the cluster does not grant today.
	have := map[string]bool{}
	for _, l := range live {
		for _, p := range permissions(l.Body) {
			have[p] = true
		}
	}
	added := map[string]bool{}
	for _, d := range desired {
		for _, p := range permissions(normalizeRBAC(d.Kind, d.Namespace, d.Name, d.Body)) {
			if !have[p] {
				added[p] = true
			}
		}
	}
	for p := range added {
		widened = append(widened, p)
	}
	sort.Strings(widened)
	return entries, widened
}

// permissions expands a normalized Role/ClusterRole into one string per
// (where, group/resource, names, verb). Bindings grant nothing by themselves.
func permissions(body map[string]any) []string {
	kind, _ := body["kind"].(string)
	if kind != "Role" && kind != "ClusterRole" {
		return nil
	}
	where := "cluster"
	if ns, _ := body["namespace"].(string); ns != "" {
		where = "namespace " + ns
	}
	var out []string
	rules, _ := body["rules"].([]any)
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		names := strings.Join(stringsOf(rule["resourceNames"]), ",")
		for _, u := range stringsOf(rule["nonResourceURLs"]) {
			for _, v := range stringsOf(rule["verbs"]) {
				out = append(out, where+": "+v+" url "+u)
			}
		}
		for _, g := range stringsOf(rule["apiGroups"]) {
			if g == "" {
				g = "core"
			}
			for _, res := range stringsOf(rule["resources"]) {
				for _, v := range stringsOf(rule["verbs"]) {
					p := where + ": " + v + " " + g + "/" + res
					if names != "" {
						p += " [" + names + "]"
					}
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// yamlLines renders a normalized object as the lines the diff compares and
// prints: the roleRef and subjects, then ONE LINE PER RULE per (group,
// resource), verbs sorted, rules sorted. Order-insensitive on purpose: a live
// object whose server-side ordering differs must not read as changed, and a
// reviewer reads "apps/deployments: create,delete,…" faster than YAML.
func yamlLines(body map[string]any) []string {
	var out []string
	if ref, ok := body["roleRef"].(map[string]any); ok {
		out = append(out, fmt.Sprintf("roleRef  %v/%v", ref["kind"], ref["name"]))
	}
	var subjects []string
	if list, ok := body["subjects"].([]any); ok {
		for _, x := range list {
			sub, _ := x.(map[string]any)
			who := fmt.Sprint(sub["name"])
			if ns, _ := sub["namespace"].(string); ns != "" {
				who = ns + "/" + who
			}
			subjects = append(subjects, fmt.Sprintf("subject  %v %s", sub["kind"], who))
		}
	}
	sort.Strings(subjects)
	out = append(out, subjects...)
	var rules []string
	rs, _ := body["rules"].([]any)
	for _, r := range rs {
		rule, _ := r.(map[string]any)
		verbs := stringsOf(rule["verbs"])
		sort.Strings(verbs)
		for _, u := range stringsOf(rule["nonResourceURLs"]) {
			rules = append(rules, fmt.Sprintf("rule     url %s: %s", u, strings.Join(verbs, ",")))
		}
		names := stringsOf(rule["resourceNames"])
		sort.Strings(names)
		for _, g := range stringsOf(rule["apiGroups"]) {
			if g == "" {
				g = "core"
			}
			for _, res := range stringsOf(rule["resources"]) {
				line := fmt.Sprintf("rule     %s/%s: %s", g, res, strings.Join(verbs, ","))
				if len(names) > 0 {
					line += "  only " + strings.Join(names, ",")
				}
				rules = append(rules, line)
			}
		}
	}
	sort.Strings(rules)
	return append(out, rules...)
}

func prefixLines(p string, lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = p + l
	}
	return out
}

// lineDiff is a full-object line diff (LCS): "  " unchanged, "- " removed,
// "+ " added. Objects are a few dozen lines, so the quadratic table is fine.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+b[j])
	}
	return out
}

// writeRBACDiff prints the review. The format is part of the command's
// contract (golden-tested): the owner reads it and CI can grep it.
//
//	Grant diff  <binding>  on context <ctx>
//	scope       <scope>   (<what it derives from>)
//	live       <where the live state was read>
//
//	+ ClusterRole forge-connect-…            new
//	    + kind: ClusterRole
//	~ Role ns/forge-connect-…                changed
//	      kind: Role
//	    - …
//	    + …
//	- Role ns/old                            removed (connect prunes it)
//	= RoleBinding ns/forge-connect-…         unchanged
//
//	newly granted permissions (N):
//	  namespace a: create apps/deployments
//	summary    +1 new  ~1 changed  -0 removed  =3 unchanged
func writeRBACDiff(out io.Writer, r connectRBAC, liveSource string, entries []rbacDiffEntry, widened []string) {
	fmt.Fprintf(out, "Grant diff  %s  on context %s\n", r.bindingName(), r.KubeContext)
	fmt.Fprintf(out, "scope      %s  (%s)\n", r.Scope.label(), r.Plan.describe())
	fmt.Fprintf(out, "subject    %s\n", subjectLine(r))
	fmt.Fprintf(out, "live       %s\n\n", liveSource)
	var created, changed, removed, same int
	notes := map[string]string{"+": "new", "~": "changed", "-": "removed (connect prunes it)", "=": "unchanged"}
	for _, e := range entries {
		fmt.Fprintf(out, "%s %s  %s\n", e.Verdict, e.Ref, notes[e.Verdict])
		for _, l := range e.Lines {
			fmt.Fprintf(out, "      %s\n", l)
		}
		switch e.Verdict {
		case "+":
			created++
		case "~":
			changed++
		case "-":
			removed++
		default:
			same++
		}
	}
	fmt.Fprintf(out, "\nnewly granted permissions (%d):\n", len(widened))
	for _, p := range widened {
		fmt.Fprintf(out, "  %s\n", p)
	}
	if len(widened) == 0 {
		fmt.Fprintf(out, "  none: this run grants nothing the cluster does not already grant\n")
	}
	fmt.Fprintf(out, "summary    +%d new  ~%d changed  -%d removed  =%d unchanged\n", created, changed, removed, same)
	if r.Plan.Cluster {
		fmt.Fprintf(out, "\n%s\n", clusterAdminWarning)
	}
}

func subjectLine(r connectRBAC) string {
	s := r.connectSubject()
	if s.Kind == subjectKindServiceAccount {
		return "ServiceAccount " + s.Namespace + "/" + s.Name + " (this binding's own token identity)"
	}
	return "User " + s.Name + " (this binding's own GCP service account)"
}

// readLiveFile reads a `--live-from` capture.
func readLiveFile(path string) ([]liveObject, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --live-from: %w", err)
	}
	return parseLive(data)
}

// stalePrune is what a re-run removes: the binding's apply-grant objects that
// the new plan no longer names (a narrowed scope must narrow the cluster).
func stalePrune(desired []rbacObject, live []liveObject) []liveObject {
	want := map[string]bool{}
	for _, d := range desired {
		want[d.Kind+"\x00"+d.Namespace+"\x00"+d.Name] = true
	}
	var out []liveObject
	for _, l := range live {
		if !l.Delivery && !want[l.Kind+"\x00"+l.Namespace+"\x00"+l.Name] {
			out = append(out, l)
		}
	}
	return out
}

// runKubectl runs kubectl with the given arguments and returns stdout; a
// failure carries stderr.
func runKubectl(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	scrubSubprocessLogEnv(cmd)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}
