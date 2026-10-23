package cli

// The control-plane DELIVERY grant: what `forge cluster connect` converges so
// the control plane can write and renew the Secrets an env DECLARES, and
// nothing else.
//
// Some Secrets in a connected cluster are not deployed with a bundle — a
// bundle never carries a secret value — but supplied and renewed by the control
// plane over the same connected-cluster credential (a telemetry token that
// must rotate before it expires, for one). That needs write access to Secrets
// in a cluster the owner operates, which is exactly the kind of grant that must
// be as narrow as it can be made. Two mechanisms, because neither alone can:
//
//   - RBAC bounds WHICH Secrets by namespace and by name. It is a Role per
//     declared namespace (not a ClusterRole: a ClusterRole's resourceNames
//     apply in EVERY namespace, so a same-named Secret anywhere would be
//     readable). get/update/patch/delete are pinned with resourceNames.
//     `create` cannot be: the API server does not know the name yet, so RBAC
//     can only allow create on Secrets in the namespace.
//   - A ValidatingAdmissionPolicy closes that gap. For the delivery
//     identities, and only them (matched by exact username, no label a
//     workload could strip), a Secret write is admitted iff its namespace/name was
//     declared AND its type is Opaque. Fail-closed: failurePolicy Fail.
//
// THE Opaque CHECK IS NOT DECORATION. Without it an identity that may create
// the declared Secret could create it as type
// kubernetes.io/service-account-token naming any ServiceAccount in that
// namespace, and the token controller would mint a credential for it — the
// env's own deploy ServiceAccount included. The name check alone does not stop
// that; it is why the policy has two validations.
//
// ORDER IS THE SAFETY PROPERTY. The policy and its binding are applied before
// the Role, so on a cluster too old for ValidatingAdmissionPolicy the first
// stage fails and the create grant never lands ungated.
//
// connect only ever ADDS. A narrower re-run replaces the policy (so writes to a
// dropped name are denied at once) but leaves the old Role in place, because
// pruning grants is a decision for the owner; `disconnect` removes all of it.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// deliveryGrantLabelKey marks the per-namespace Role and RoleBinding so
	// disconnect can find them without being told which namespaces were declared.
	deliveryGrantLabelKey   = "forge.dev/connect-grant"
	deliveryGrantLabelValue = "delivery"

	subjectKindUser           = "User"
	subjectKindServiceAccount = "ServiceAccount"
)

var (
	// DNS-1123 label / subdomain. These strings are interpolated into a CEL
	// expression and into RBAC resourceNames, so the grammar is enforced rather
	// than escaped: an invalid name is refused, never rendered.
	deliveryNamespaceRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	deliveryNameRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// An RBAC username: a GSA email, an OIDC subject, system:serviceaccount:….
	deliveryUserRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:+-]{0,252}$`)
)

// deliverySecret is one Secret the control plane may write.
type deliverySecret struct {
	Namespace string
	Name      string
}

// deliverySubject is an RBAC subject. It is also how the connect identity is
// expressed when the grant needs its username.
type deliverySubject struct {
	Kind      string // subjectKindUser | subjectKindServiceAccount
	Name      string
	Namespace string // ServiceAccount only
}

// username is the name the apiserver authenticates, which is what the
// admission policy matches.
func (s deliverySubject) username() string {
	if s.Kind == subjectKindServiceAccount {
		return "system:serviceaccount:" + s.Namespace + ":" + s.Name
	}
	return s.Name
}

// parseDeliverySecrets turns repeated `--delivery-secret <namespace>/<name>`
// values into a sorted, de-duplicated set.
func parseDeliverySecrets(specs []string) ([]deliverySecret, error) {
	seen := map[deliverySecret]bool{}
	var out []deliverySecret
	for _, spec := range specs {
		ns, name, ok := strings.Cut(strings.TrimSpace(spec), "/")
		if !ok || !deliveryNamespaceRE.MatchString(ns) || !deliveryNameRE.MatchString(name) {
			return nil, fmt.Errorf("--delivery-secret %q is not <namespace>/<name> with DNS-1123 names\n"+
				"fix: name the exact Secret, e.g. --delivery-secret control-plane-prod/reliant-otel-auth", spec)
		}
		s := deliverySecret{Namespace: ns, Name: name}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// parseDeliverySubject reads `--delivery-subject`: `user:<name>` (a GCP
// service account email on a GKE cluster) or `serviceaccount:<namespace>/<name>`.
func parseDeliverySubject(spec string) (*deliverySubject, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	bad := fmt.Errorf("--delivery-subject %q is not user:<name> or serviceaccount:<namespace>/<name>\n"+
		"fix: e.g. --delivery-subject user:delivery@acme.iam.gserviceaccount.com", spec)
	kind, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, bad
	}
	switch strings.ToLower(kind) {
	case "user":
		if !deliveryUserRE.MatchString(rest) {
			return nil, bad
		}
		return &deliverySubject{Kind: subjectKindUser, Name: rest}, nil
	case "serviceaccount":
		ns, name, ok := strings.Cut(rest, "/")
		if !ok || !deliveryNamespaceRE.MatchString(ns) || !deliveryNameRE.MatchString(name) {
			return nil, bad
		}
		return &deliverySubject{Kind: subjectKindServiceAccount, Namespace: ns, Name: name}, nil
	}
	return nil, bad
}

// deliveryEnabled is false unless the owner named a Secret, so a connect with
// no --delivery-secret widens nothing.
func (r connectRBAC) deliveryEnabled() bool { return len(r.DeliverySecrets) > 0 }

// connectSubject is the connect identity as a subject.
func (r connectRBAC) connectSubject() deliverySubject {
	if r.Auth == authToken {
		return deliverySubject{Kind: subjectKindServiceAccount, Namespace: r.TokenNamespace, Name: r.tokenServiceAccount()}
	}
	return deliverySubject{Kind: subjectKindUser, Name: r.BindingGSA}
}

// deliverySubjects are who the grant binds: the connect identity, plus the
// optional dedicated delivery identity.
func (r connectRBAC) deliverySubjects() []deliverySubject {
	subjects := []deliverySubject{r.connectSubject()}
	if r.DeliverySubject != nil {
		subjects = append(subjects, *r.DeliverySubject)
	}
	return subjects
}

// validateDelivery refuses a grant that cannot be rendered safely.
func (r connectRBAC) validateDelivery() error {
	if r.DeliverySubject != nil && !r.deliveryEnabled() {
		return fmt.Errorf("--delivery-subject has nothing to grant: name the Secrets with --delivery-secret <namespace>/<name>")
	}
	return nil
}

// validateConnectIdentity runs once the connect identity is known (a gcp
// GSA comes from the control plane's response and ends up inside a CEL
// expression).
func (r connectRBAC) validateConnectIdentity() error {
	if !r.deliveryEnabled() {
		return nil
	}
	if u := r.connectSubject().username(); !deliveryUserRE.MatchString(u) {
		return fmt.Errorf("the connect identity %q is not a username forge will place in an admission policy", u)
	}
	return nil
}

func deliveryObjectName(clusterName, env string) string {
	return connectBindingName(clusterName, env) + "-delivery"
}

// deliveryLabels is the label block (already indented as metadata children).
func (r connectRBAC) deliveryLabels() string {
	return "  labels:\n" +
		"    " + forgeManagedLabelKey + ": " + forgeManagedLabelValue + "\n" +
		"    " + connectClusterLabelKey + ": " + strconv.Quote(r.ClusterName) + "\n" +
		"    " + connectBindingLabelKey + ": " + strconv.Quote(dnsSegment(r.Env)) + "\n" +
		"    " + deliveryGrantLabelKey + ": " + deliveryGrantLabelValue + "\n"
}

// celString renders s as a single-quoted CEL string literal.
func celString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func celStringList(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = celString(it)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// deliveryGuardManifests is the ValidatingAdmissionPolicy and its binding.
func (r connectRBAC) deliveryGuardManifests() []string {
	name := deliveryObjectName(r.ClusterName, r.Env)

	var usernames []string
	for _, s := range r.deliverySubjects() {
		usernames = append(usernames, s.username())
	}
	var declared []string
	for _, s := range r.DeliverySecrets {
		declared = append(declared, s.Namespace+"/"+s.Name)
	}

	// request.name is empty for a generateName create, which is therefore
	// refused — the right outcome, since it names nothing declared.
	nameCheck := "(request.namespace + '/' + request.name) in " + celStringList(declared)
	// object is null on DELETE, so the type is only asserted when there is one.
	typeCheck := "request.operation == 'DELETE' || object.type == 'Opaque'"

	policy := "apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingAdmissionPolicy\nmetadata:\n  name: " + name + "\n" +
		r.deliveryLabels() +
		"spec:\n" +
		"  failurePolicy: Fail\n" +
		"  matchConstraints:\n" +
		"    resourceRules:\n" +
		"    - apiGroups: [\"\"]\n" +
		"      apiVersions: [\"v1\"]\n" +
		"      resources: [\"secrets\"]\n" +
		"      operations: [\"CREATE\", \"UPDATE\", \"DELETE\"]\n" +
		"  matchConditions:\n" +
		"  - name: only-the-delivery-identities\n" +
		"    expression: " + strconv.Quote("request.userInfo.username in "+celStringList(usernames)) + "\n" +
		"  validations:\n" +
		"  - expression: " + strconv.Quote(nameCheck) + "\n" +
		"    message: " + strconv.Quote("the control plane's delivery identity may write only the Secrets this cluster was connected to deliver: "+strings.Join(declared, ", ")) + "\n" +
		"    reason: Forbidden\n" +
		"  - expression: " + strconv.Quote(typeCheck) + "\n" +
		"    message: " + strconv.Quote("delivered Secrets must be type Opaque: a service-account-token Secret would mint a credential for a ServiceAccount") + "\n" +
		"    reason: Forbidden\n"

	binding := "apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingAdmissionPolicyBinding\nmetadata:\n  name: " + name + "\n" +
		r.deliveryLabels() +
		"spec:\n  policyName: " + name + "\n  validationActions: [\"Deny\", \"Audit\"]\n"

	return []string{strings.TrimRight(policy, "\n"), strings.TrimRight(binding, "\n")}
}

// deliveryGrantManifests is one Role and RoleBinding per declared namespace.
func (r connectRBAC) deliveryGrantManifests() []string {
	byNamespace := map[string][]string{}
	var namespaces []string
	for _, s := range r.DeliverySecrets { // sorted by parseDeliverySecrets
		if _, ok := byNamespace[s.Namespace]; !ok {
			namespaces = append(namespaces, s.Namespace)
		}
		byNamespace[s.Namespace] = append(byNamespace[s.Namespace], s.Name)
	}
	sort.Strings(namespaces)

	name := deliveryObjectName(r.ClusterName, r.Env)
	var subjects strings.Builder
	subjects.WriteString("subjects:\n")
	for _, s := range r.deliverySubjects() {
		subjects.WriteString("- kind: " + s.Kind + "\n  name: " + strconv.Quote(s.Name) + "\n")
		if s.Kind == subjectKindServiceAccount {
			subjects.WriteString("  namespace: " + strconv.Quote(s.Namespace) + "\n")
		} else {
			subjects.WriteString("  apiGroup: rbac.authorization.k8s.io\n")
		}
	}

	var docs []string
	for _, ns := range namespaces {
		names := make([]string, len(byNamespace[ns]))
		for i, n := range byNamespace[ns] {
			names[i] = strconv.Quote(n)
		}
		role := "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: " + name + "\n  namespace: " + ns + "\n" +
			r.deliveryLabels() +
			"rules:\n" +
			// delete is how the control plane revokes a delivered Secret when
			// the binding is removed; it is name-pinned like the rest.
			"- apiGroups: [\"\"]\n  resources: [\"secrets\"]\n  verbs: [\"get\", \"update\", \"patch\", \"delete\"]\n" +
			"  resourceNames: [" + strings.Join(names, ", ") + "]\n" +
			// RBAC cannot pin create to a name; the admission policy does.
			"- apiGroups: [\"\"]\n  resources: [\"secrets\"]\n  verbs: [\"create\"]\n"
		binding := "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: " + name + "\n  namespace: " + ns + "\n" +
			r.deliveryLabels() +
			"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: " + name + "\n" +
			subjects.String()
		docs = append(docs, strings.TrimRight(role, "\n"), strings.TrimRight(binding, "\n"))
	}
	return docs
}
