package cli

import (
	"reflect"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func deliveryRBAC(auth string, subject *deliverySubject, secrets ...string) connectRBAC {
	parsed, err := parseDeliverySecrets(secrets)
	if err != nil {
		panic(err)
	}
	return connectRBAC{
		ClusterName: "prod-us", Env: "prod", Auth: auth,
		BindingGSA:      "binding@acme.iam.gserviceaccount.com",
		TokenNamespace:  connectTokenNamespace,
		DeliverySecrets: parsed, DeliverySubject: subject,
	}
}

func splitDocs(s string) []string {
	var out []string
	for _, d := range strings.Split(s, "\n---\n") {
		if strings.TrimSpace(d) != "" {
			out = append(out, d)
		}
	}
	return out
}

// ── flag parsing ────────────────────────────────────────────────────────────

func TestParseDeliverySecrets(t *testing.T) {
	got, err := parseDeliverySecrets([]string{"b/z", "a/y", "a/x", "a/y"})
	if err != nil {
		t.Fatal(err)
	}
	want := []deliverySecret{{"a", "x"}, {"a", "y"}, {"b", "z"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want sorted and de-duplicated %v", got, want)
	}
	// Anything that could break out of a CEL string or an RBAC list is refused,
	// never escaped.
	for _, bad := range []string{"", "ns", "ns/", "/n", "NS/n", "ns/n'x", `ns/n"x`, "ns/n x", "a/b/c", "ns/*", "ns/-n"} {
		if _, err := parseDeliverySecrets([]string{bad}); err == nil {
			t.Errorf("parseDeliverySecrets(%q) accepted a malformed value", bad)
		}
	}
}

func TestParseDeliverySubject(t *testing.T) {
	for spec, want := range map[string]*deliverySubject{
		"":                                    nil,
		"user:d@acme.iam.gserviceaccount.com": {Kind: subjectKindUser, Name: "d@acme.iam.gserviceaccount.com"},
		"serviceaccount:cp/delivery":          {Kind: subjectKindServiceAccount, Namespace: "cp", Name: "delivery"},
	} {
		got, err := parseDeliverySubject(spec)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseDeliverySubject(%q) = %v, %v; want %v", spec, got, err, want)
		}
	}
	for _, bad := range []string{"d@acme.com", "group:x", "user:", "user:a'b", "serviceaccount:ns", "serviceaccount:/n"} {
		if _, err := parseDeliverySubject(bad); err == nil {
			t.Errorf("parseDeliverySubject(%q) accepted a malformed value", bad)
		}
	}
}

func TestValidateDelivery_SubjectWithoutSecretsIsRefused(t *testing.T) {
	r := connectRBAC{DeliverySubject: &deliverySubject{Kind: subjectKindUser, Name: "x"}}
	if err := r.validateDelivery(); err == nil || !strings.Contains(err.Error(), "--delivery-secret") {
		t.Errorf("err = %v, want a refusal naming --delivery-secret", err)
	}
}

// ── rendering ───────────────────────────────────────────────────────────────

// Not asking for delivery must change nothing: the existing bootstrap is
// byte-identical and there is exactly one stage.
func TestDelivery_OffByDefaultWidensNothing(t *testing.T) {
	r := deliveryRBAC(authToken, nil)
	if got := r.bootstrapStages(); len(got) != 1 {
		t.Fatalf("got %d stages without --delivery-secret, want 1", len(got))
	}
	if strings.Contains(r.bootstrapManifests(), "ValidatingAdmissionPolicy") ||
		strings.Contains(r.bootstrapManifests(), deliveryGrantLabelKey+": "+deliveryGrantLabelValue) {
		t.Error("the grant carries delivery objects although none were requested")
	}
}

// The policy must precede the Roles, in separate applies.
func TestDelivery_StagesPutThePolicyBeforeTheRoles(t *testing.T) {
	stages := deliveryRBAC(authGCP, nil, "ns/a").bootstrapStages()
	if len(stages) != 3 {
		t.Fatalf("got %d stages, want base, policy, roles", len(stages))
	}
	if !strings.Contains(stages[1], "kind: ValidatingAdmissionPolicy\n") || strings.Contains(stages[1], "kind: Role\n") {
		t.Errorf("stage 2 is not the policy alone:\n%s", stages[1])
	}
	if !strings.Contains(stages[2], "kind: Role\n") || strings.Contains(stages[2], "ValidatingAdmissionPolicy") {
		t.Errorf("stage 3 is not the roles alone:\n%s", stages[2])
	}
}

func TestDelivery_RolesAreNamespacedAndNamePinned(t *testing.T) {
	r := deliveryRBAC(authToken, nil, "control-plane-prod/reliant-otel-auth", "control-plane-prod/other", "obs/agent-token")
	docs := splitDocs(strings.Join(r.deliveryGrantManifests(), "\n---\n"))
	if len(docs) != 4 {
		t.Fatalf("got %d objects, want a Role and RoleBinding for each of 2 namespaces", len(docs))
	}
	roles := map[string]rbacv1.Role{}
	var bindings []rbacv1.RoleBinding
	for _, d := range docs {
		var meta struct{ Kind string }
		if err := yaml.Unmarshal([]byte(d), &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "Role":
			var role rbacv1.Role
			if err := yaml.UnmarshalStrict([]byte(d), &role); err != nil {
				t.Fatalf("%v\n%s", err, d)
			}
			roles[role.Namespace] = role
		case "RoleBinding":
			var rb rbacv1.RoleBinding
			if err := yaml.UnmarshalStrict([]byte(d), &rb); err != nil {
				t.Fatalf("%v\n%s", err, d)
			}
			bindings = append(bindings, rb)
		default:
			t.Fatalf("unexpected kind %q", meta.Kind)
		}
	}
	cp := roles["control-plane-prod"]
	if len(cp.Rules) != 2 {
		t.Fatalf("rules = %+v, want a name-pinned rule and a create rule", cp.Rules)
	}
	pinned, create := cp.Rules[0], cp.Rules[1]
	if !reflect.DeepEqual(pinned.Verbs, []string{"get", "update", "patch", "delete"}) ||
		!reflect.DeepEqual(pinned.Resources, []string{"secrets"}) ||
		!reflect.DeepEqual(pinned.ResourceNames, []string{"other", "reliant-otel-auth"}) {
		t.Errorf("pinned rule = %+v", pinned)
	}
	if !reflect.DeepEqual(create.Verbs, []string{"create"}) || len(create.ResourceNames) != 0 {
		t.Errorf("create rule = %+v (RBAC cannot name-pin create)", create)
	}
	// No verb that reads or lists every Secret in the namespace.
	for _, rule := range cp.Rules {
		for _, v := range rule.Verbs {
			if v == "list" || v == "watch" || v == "*" || v == "deletecollection" {
				t.Errorf("rule grants %q", v)
			}
		}
	}
	if !reflect.DeepEqual(roles["obs"].Rules[0].ResourceNames, []string{"agent-token"}) {
		t.Errorf("obs role = %+v", roles["obs"])
	}
	for _, rb := range bindings {
		if rb.RoleRef.Kind != "Role" {
			t.Errorf("binding refs %s %s, want a namespaced Role, never a ClusterRole", rb.RoleRef.Kind, rb.RoleRef.Name)
		}
	}
}

func TestDelivery_SubjectsByAuthAndOptionalSecondSubject(t *testing.T) {
	second := &deliverySubject{Kind: subjectKindUser, Name: "delivery@acme.iam.gserviceaccount.com"}
	for _, tc := range []struct {
		name string
		r    connectRBAC
		want []rbacv1.Subject
	}{
		{"token", deliveryRBAC(authToken, nil, "ns/a"), []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "forge-connect-prod-us-prod", Namespace: "forge-system"}}},
		{"gcp", deliveryRBAC(authGCP, nil, "ns/a"), []rbacv1.Subject{
			{Kind: "User", Name: "binding@acme.iam.gserviceaccount.com", APIGroup: "rbac.authorization.k8s.io"}}},
		{"gcp plus a dedicated identity", deliveryRBAC(authGCP, second, "ns/a"), []rbacv1.Subject{
			{Kind: "User", Name: "binding@acme.iam.gserviceaccount.com", APIGroup: "rbac.authorization.k8s.io"},
			{Kind: "User", Name: "delivery@acme.iam.gserviceaccount.com", APIGroup: "rbac.authorization.k8s.io"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := tc.r.deliveryGrantManifests()
			var rb rbacv1.RoleBinding
			if err := yaml.UnmarshalStrict([]byte(docs[1]), &rb); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rb.Subjects, tc.want) {
				t.Errorf("subjects = %+v, want %+v", rb.Subjects, tc.want)
			}
		})
	}
}

func TestDelivery_EverythingIsLabelledForDisconnect(t *testing.T) {
	r := deliveryRBAC(authToken, nil, "ns/a", "other/b")
	for _, doc := range append(r.deliveryGuardManifests(), r.deliveryGrantManifests()...) {
		var obj struct {
			Metadata struct{ Labels map[string]string }
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		l := obj.Metadata.Labels
		if l[forgeManagedLabelKey] != forgeManagedLabelValue || l[connectClusterLabelKey] != "prod-us" || l[connectBindingLabelKey] != "prod" ||
			l[deliveryGrantLabelKey] != deliveryGrantLabelValue {
			t.Errorf("labels = %v\n%s", l, doc)
		}
	}
}

// Re-running connect with the same inputs renders the same bytes (the
// idempotent half; server-side apply does the rest), whatever the flag order.
func TestDelivery_RenderIsDeterministicAndOrderInsensitive(t *testing.T) {
	a := deliveryRBAC(authToken, nil, "b/z", "a/y", "a/x").bootstrapManifests()
	b := deliveryRBAC(authToken, nil, "a/x", "a/y", "b/z").bootstrapManifests()
	if a != b {
		t.Errorf("flag order changed the rendered grant:\n%s\n---\n%s", a, b)
	}
}

// ── the admission policy, evaluated ─────────────────────────────────────────

func loadDeliveryPolicy(t *testing.T, r connectRBAC) (*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding) {
	t.Helper()
	docs := r.deliveryGuardManifests()
	var p admissionv1.ValidatingAdmissionPolicy
	var b admissionv1.ValidatingAdmissionPolicyBinding
	if err := yaml.UnmarshalStrict([]byte(docs[0]), &p); err != nil {
		t.Fatalf("policy does not parse as admissionregistration/v1: %v\n%s", err, docs[0])
	}
	if err := yaml.UnmarshalStrict([]byte(docs[1]), &b); err != nil {
		t.Fatalf("binding does not parse: %v\n%s", err, docs[1])
	}
	return &p, &b
}

func TestDelivery_PolicyShape(t *testing.T) {
	p, b := loadDeliveryPolicy(t, deliveryRBAC(authToken, nil, "ns/a"))
	if p.Spec.FailurePolicy == nil || *p.Spec.FailurePolicy != admissionv1.Fail {
		t.Error("the policy is not fail-closed")
	}
	if b.Spec.PolicyName != p.Name {
		t.Errorf("binding names %q, policy is %q", b.Spec.PolicyName, p.Name)
	}
	hasDeny := false
	for _, a := range b.Spec.ValidationActions {
		hasDeny = hasDeny || a == admissionv1.Deny
	}
	if !hasDeny {
		t.Errorf("binding actions %v do not Deny", b.Spec.ValidationActions)
	}
	rr := p.Spec.MatchConstraints.ResourceRules
	if len(rr) != 1 || !reflect.DeepEqual(rr[0].Resources, []string{"secrets"}) || len(rr[0].Operations) != 3 {
		t.Errorf("match constraints = %+v, want secrets CREATE/UPDATE/DELETE", rr)
	}
}

type admissionRequest struct {
	user, ns, name, op, typ string
}

// evalPolicy returns whether the policy would admit the request, evaluating the
// rendered CEL exactly as the apiserver's variables define it: matchConditions
// must all be true for the policy to apply at all, then every validation must
// be true.
func evalPolicy(t *testing.T, p *admissionv1.ValidatingAdmissionPolicy, req admissionRequest) (applies, admitted bool, msg string) {
	t.Helper()
	env, err := cel.NewEnv(cel.Variable("request", cel.DynType), cel.Variable("object", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	var object any
	if req.op != "DELETE" {
		object = map[string]any{"type": req.typ}
	}
	act := map[string]any{
		"request": map[string]any{
			"userInfo":  map[string]any{"username": req.user},
			"namespace": req.ns, "name": req.name, "operation": req.op,
		},
		"object": object,
	}
	run := func(expr string) bool {
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			t.Fatalf("CEL does not compile: %v\n%s", iss.Err(), expr)
		}
		prg, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := prg.Eval(act)
		if err != nil {
			return false // a runtime error under failurePolicy Fail denies
		}
		b, ok := out.Value().(bool)
		return ok && b
	}
	for _, mc := range p.Spec.MatchConditions {
		if !run(mc.Expression) {
			return false, true, ""
		}
	}
	for _, v := range p.Spec.Validations {
		if !run(v.Expression) {
			return true, false, v.Message
		}
	}
	return true, true, ""
}

func TestDelivery_PolicyAdmitsOnlyDeclaredSecretsAsOpaque(t *testing.T) {
	second := &deliverySubject{Kind: subjectKindUser, Name: "delivery@acme.iam.gserviceaccount.com"}
	r := deliveryRBAC(authToken, second, "control-plane-prod/reliant-otel-auth", "obs/agent-token")
	p, _ := loadDeliveryPolicy(t, r)
	connect := "system:serviceaccount:forge-system:forge-connect-prod-us-prod"
	const delivery = "delivery@acme.iam.gserviceaccount.com"

	for _, tc := range []struct {
		name        string
		req         admissionRequest
		wantApplies bool
		wantAdmit   bool
	}{
		{"declared secret, connect identity", admissionRequest{connect, "control-plane-prod", "reliant-otel-auth", "CREATE", "Opaque"}, true, true},
		{"declared secret, dedicated identity", admissionRequest{delivery, "obs", "agent-token", "UPDATE", "Opaque"}, true, true},
		{"declared secret, delete", admissionRequest{delivery, "obs", "agent-token", "DELETE", ""}, true, true},
		{"undeclared name, same namespace", admissionRequest{connect, "control-plane-prod", "db-password", "CREATE", "Opaque"}, true, false},
		{"declared name, wrong namespace", admissionRequest{connect, "kube-system", "reliant-otel-auth", "CREATE", "Opaque"}, true, false},
		{"declared name in the OTHER declared namespace", admissionRequest{connect, "obs", "reliant-otel-auth", "CREATE", "Opaque"}, true, false},
		{"generateName create (empty name)", admissionRequest{connect, "obs", "", "CREATE", "Opaque"}, true, false},
		{"name that merely contains the slash form", admissionRequest{connect, "control-plane-prod/reliant-otel-auth", "x", "CREATE", "Opaque"}, true, false},
		// The reason for the second validation.
		{"declared secret as a service-account token", admissionRequest{connect, "obs", "agent-token", "CREATE", "kubernetes.io/service-account-token"}, true, false},
		{"declared secret as dockerconfigjson", admissionRequest{connect, "obs", "agent-token", "UPDATE", "kubernetes.io/dockerconfigjson"}, true, false},
		// Not a delivery identity: the policy must not touch anyone else.
		{"another user writing any secret", admissionRequest{"alice", "kube-system", "anything", "CREATE", "Opaque"}, false, true},
		{"the hub deploy identity", admissionRequest{"system:serviceaccount:flux-acme:deploy", "kube-system", "anything", "CREATE", "Opaque"}, false, true},
		{"username prefix-matching an identity", admissionRequest{connect + "-evil", "kube-system", "x", "CREATE", "Opaque"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applies, admitted, msg := evalPolicy(t, p, tc.req)
			if applies != tc.wantApplies || admitted != tc.wantAdmit {
				t.Errorf("applies=%v admitted=%v (%s), want applies=%v admitted=%v", applies, admitted, msg, tc.wantApplies, tc.wantAdmit)
			}
		})
	}
}

// Declared names appear nowhere except as quoted data in the rendered CEL, and
// a GSA subject cannot smuggle an expression in through the connect identity.
func TestDelivery_ConnectIdentityIsValidatedBeforeItReachesCEL(t *testing.T) {
	r := deliveryRBAC(authGCP, nil, "ns/a")
	r.BindingGSA = "x' || true || '"
	if err := r.validateConnectIdentity(); err == nil {
		t.Error("a connect identity containing a quote was accepted")
	}
	r.BindingGSA = "binding@acme.iam.gserviceaccount.com"
	if err := r.validateConnectIdentity(); err != nil {
		t.Error(err)
	}
}
