package v1alpha1

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

const pinnedImage = "ghcr.io/acme/api:v1.4.2"

func svc() WorkloadSpec {
	return WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 8080, Expose: true}}}
}

func mustPass(t *testing.T, s WorkloadSpec, p Profile) {
	t.Helper()
	if err := s.Validate(p); err != nil {
		t.Fatalf("Validate(%s) = %v, want nil", p, err)
	}
}

// TestWorkloadValidateMinimalPerKind: every kind has a minimal spec that is
// valid under Full, so the structural rules never refuse a legitimate
// declaration of any kind.
func TestWorkloadValidateMinimalPerKind(t *testing.T) {
	for name, s := range map[string]WorkloadSpec{
		"service (kind defaulted)": svc(),
		"worker":                   {Kind: KindWorker, Image: pinnedImage},
		"job":                      {Kind: KindJob, Image: pinnedImage, Args: []string{"migrate"}},
		"cron":                     {Kind: KindCron, Image: pinnedImage, Command: []string{"/app/sweep"}, Schedule: "*/5 * * * *"},
		"operator":                 {Kind: KindOperator, Image: pinnedImage, CRDs: []string{"Widget"}},
		// Before its first CRD (`forge scaffold operator`, then `forge
		// scaffold crd`), or reconciling built-in kinds only.
		"operator owning no CRD":   {Kind: KindOperator, Image: pinnedImage},
		"tool":                     {Kind: KindTool, Image: pinnedImage},
		"bare local image on Full": {Image: "api:dev"},
	} {
		t.Run(name, func(t *testing.T) { mustPass(t, s, ProfileFull) })
	}
}

// TestWorkloadValidateStructuralRules covers every rule that holds under
// EVERY profile. Each case runs under both profiles, so a structural rule
// can never be one that only Restricted happens to enforce.
func TestWorkloadValidateStructuralRules(t *testing.T) {
	cases := map[string]struct {
		spec WorkloadSpec
		want string
	}{
		"image required":             {WorkloadSpec{}, "image is required"},
		"image whitespace":           {WorkloadSpec{Image: "ghcr.io/a b:v1"}, "whitespace"},
		"unknown kind":               {WorkloadSpec{Kind: "daemon", Image: pinnedImage}, `kind "daemon" must be one of`},
		"storage requires 1 replica": {WorkloadSpec{Image: pinnedImage, Replicas: 2, StorageGiB: 5}, "storageGiB requires replicas 1 (got 2)"},
		"negative storage":           {WorkloadSpec{Image: pinnedImage, StorageGiB: -1}, "storageGiB must not be negative"},
		"negative replicas":          {WorkloadSpec{Image: pinnedImage, Replicas: -1}, "replicas must not be negative"},
		"schedule only for cron":     {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily"}, `schedule is not supported for kind "job"`},
		"cron needs a schedule":      {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}}, "kind cron requires a schedule"},
		"cron needs a command":       {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Schedule: "@daily"}, "kind cron requires command or args"},
		"bad schedule":               {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "every day"}, "5-field cron expression"},
		"six-field schedule":         {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "0 * * * * *"}, "5-field cron expression"},
		"before only for job":        {WorkloadSpec{Image: pinnedImage, Before: []string{"api"}}, `before is not supported for kind "service"`},
		"job needs command or args":  {WorkloadSpec{Kind: KindJob, Image: pinnedImage}, "kind job requires command or args"},
		"before names a workload":    {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"API"}}, "RFC-1123"},
		"before wildcard is alone":   {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"*", "api"}}, "already gates every workload"},
		"before duplicates":          {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api", "api"}}, "listed twice"},
		"deployPhase only for job":   {WorkloadSpec{Image: pinnedImage, DeployPhase: DeployPhasePostRollout}, `deployPhase is not supported for kind "service"`},
		"deployPhase not with before": {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Before: []string{"api"}, DeployPhase: DeployPhasePreRollout},
			"deployPhase is only for a standalone job"},
		"deployPhase enum":        {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, DeployPhase: "during"}, "must be pre-rollout or post-rollout"},
		"crds only for operator":  {WorkloadSpec{Image: pinnedImage, CRDs: []string{"Widget"}}, `crds is not supported for kind "service"`},
		"group only for operator": {WorkloadSpec{Image: pinnedImage, Group: "acme.dev"}, `group is not supported for kind "service"`},
		"leaderElection only for operator": {WorkloadSpec{Image: pinnedImage, LeaderElection: ptr(true)},
			`leaderElection is not supported for kind "service"`},
		"clusterRBAC not for a tool": {WorkloadSpec{Kind: KindTool, Image: pinnedImage, ClusterRBAC: []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}},
			`clusterRBAC is not supported for kind "tool"`},
		"namespacedRBAC not for a tool": {WorkloadSpec{Kind: KindTool, Image: pinnedImage, NamespacedRBAC: []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}},
			`namespacedRBAC is not supported for kind "tool"`},
		"rbac rule needs verbs": {WorkloadSpec{Image: pinnedImage, NamespacedRBAC: []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}}}},
			"namespacedRBAC[0].verbs must list at least one verb"},
		"ports not for job":    {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Ports: []Port{{Name: "http", Port: 80}}}, `ports is not supported for kind "job"`},
		"ports not for cron":   {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily", Ports: []Port{{Name: "http", Port: 80}}}, `ports is not supported for kind "cron"`},
		"ports not for tool":   {WorkloadSpec{Kind: KindTool, Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80}}}, `ports is not supported for kind "tool"`},
		"replicas not for job": {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Replicas: 3}, `replicas is not supported for kind "job"`},
		"port name unique": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80}, {Name: "http", Port: 81}}},
			`port name "http" is declared twice`},
		"port number unique": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "a", Port: 80}, {Name: "b", Port: 80, Protocol: ProtocolTCP}}},
			"80/tcp is declared twice"},
		"port name grammar":  {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "HTTP_PORT", Port: 80}}}, "at most 15 lowercase"},
		"port name length":   {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "a-very-long-port-name", Port: 80}}}, "at most 15 lowercase"},
		"port range":         {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 70000}}}, "must be 1-65535"},
		"port protocol enum": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Protocol: "sctp"}}}, "must be tcp or udp"},
		"expose only for service": {WorkloadSpec{Kind: KindWorker, Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Expose: true}}},
			"expose is only for kind service"},
		"one exposed port": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "a", Port: 80, Expose: true}, {Name: "b", Port: 81, Expose: true}}},
			"at most one may"},
		"probes need a port":   {WorkloadSpec{Image: pinnedImage, Probes: &Probes{}}, "probes need a port"},
		"probes not for job":   {WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"x"}, Probes: &Probes{Port: 80}}, `probes is not supported for kind "job"`},
		"tcp probe with paths": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80}}, Probes: &Probes{TCP: true, ReadinessPath: "/readyz"}}, "probes.tcp"},
		"relative probe path":  {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80}}, Probes: &Probes{LivenessPath: "healthz"}}, "probes.livenessPath"},
		"negative timings":     {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80}}, Probes: &Probes{PeriodSeconds: -1}}, "must not be negative"},
		"two env channels":     {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "X", Value: "1", FieldRef: &FieldRef{FieldPath: "metadata.name"}}}}, "more than one"},
		"configMapRef needs key": {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "X", ConfigMapRef: &ConfigMapKeyRef{Name: "c"}}}},
			"configMapRef needs both name and key"},
		"fieldRef needs a path": {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "X", FieldRef: &FieldRef{}}}}, "fieldRef needs a fieldPath"},
		"dup env":               {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "X"}, {Name: "X"}}}, "declared twice"},
		"limit below request": {WorkloadSpec{Image: pinnedImage, Resources: Resources{CPURequestMillicores: 500, CPULimitMillicores: 250}},
			"at least the request"},
		"bad SA annotation": {WorkloadSpec{Image: pinnedImage, ServiceAccountAnnotations: map[string]string{"Not A Key": "x"}}, "serviceAccountAnnotations key"},
		"SA annotations not for tool": {WorkloadSpec{Kind: KindTool, Image: pinnedImage, ServiceAccountAnnotations: map[string]string{"a": "b"}},
			`serviceAccountAnnotations is not supported for kind "tool"`},
	}
	for name, c := range cases {
		for _, p := range []Profile{ProfileFull, ProfileRestricted} {
			t.Run(name+"/"+p.String(), func(t *testing.T) {
				mustFail(t, c.spec.Validate(p), c.want)
			})
		}
	}

	// Collected, not first-error-only: one pass surfaces every mistake.
	bad := WorkloadSpec{Kind: KindJob, Replicas: 2, StorageGiB: -1}
	if n := strings.Count(bad.Validate(ProfileFull).Error(), "\n") + 1; n < 4 {
		t.Errorf("want every violation reported together, got %d: %v", n, bad.Validate(ProfileFull))
	}
}

// TestWorkloadValidateRestrictedProfile: Restricted accepts exactly the ADR
// §3 subset and refuses each Full-only field BY NAME, with its reason.
func TestWorkloadValidateRestrictedProfile(t *testing.T) {
	// Everything Restricted allows, set at once.
	allowed := WorkloadSpec{
		Kind: KindService, Image: pinnedImage, Command: []string{"/app/api"}, Args: []string{"serve"}, Replicas: 1,
		Resources: Resources{CPURequestMillicores: 500, MemoryRequestBytes: 2 << 30},
		Env: []EnvVar{
			{Name: "A", Value: "1"},
			{Name: "B", ManagedSecret: &ManagedSecretRef{Name: "B"}},
			{Name: "B2", ManagedSecret: &ManagedSecretRef{Name: "B2", Optional: true}},
			{Name: "C", DatabaseRef: &DatabaseRef{Name: "orders"}},
			{Name: "D", WorkloadURL: &WorkloadURLRef{Name: "web"}},
		},
		Ports:      []Port{{Name: "http", Port: 8080, Protocol: ProtocolTCP, Expose: true}},
		Probes:     &Probes{Port: 8080, ReadinessPath: "/readyz", LivenessPath: "/healthz", InitialDelaySeconds: 1, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 3},
		StorageGiB: 5,
	}
	mustPass(t, allowed, ProfileRestricted)
	mustPass(t, WorkloadSpec{Kind: KindWorker, Image: pinnedImage, Replicas: 3}, ProfileRestricted)
	mustPass(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"migrate"}, Before: []string{BeforeAll}}, ProfileRestricted)
	mustPass(t, WorkloadSpec{Kind: KindJob, Image: pinnedImage, Args: []string{"seed"}, DeployPhase: DeployPhasePostRollout}, ProfileRestricted)

	cases := map[string]struct {
		spec WorkloadSpec
		want string
	}{
		"cron kind": {WorkloadSpec{Kind: KindCron, Image: pinnedImage, Args: []string{"x"}, Schedule: "@daily"}, `kind "cron" is not allowed under the restricted profile: cron is not available on hosted until metering covers it`},
		"operator kind": {WorkloadSpec{Kind: KindOperator, Image: pinnedImage, CRDs: []string{"W"}},
			`kind "operator" is not allowed under the restricted profile`},
		"tool kind": {WorkloadSpec{Kind: KindTool, Image: pinnedImage}, `kind "tool" is not allowed under the restricted profile`},
		"namespacedRBAC": {WorkloadSpec{Image: pinnedImage, NamespacedRBAC: []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}},
			"namespacedRBAC: not allowed under the restricted profile: hosted workloads have no Kubernetes API access until each hosted user runs in an isolated cluster (vcluster)"},
		"serviceAccountAnnotations": {WorkloadSpec{Image: pinnedImage, ServiceAccountAnnotations: map[string]string{"iam.gke.io/gcp-service-account": "x"}},
			"serviceAccountAnnotations: not allowed under the restricted profile"},
		"secretRef": {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "PW", SecretRef: &SecretKeyRef{Name: "platform-db", Key: "pw"}}}},
			"env[PW].secretRef: not allowed under the restricted profile: a raw Secret name"},
		"configMapRef": {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "F", ConfigMapRef: &ConfigMapKeyRef{Name: "c", Key: "k"}}}},
			"env[F].configMapRef: not allowed under the restricted profile"},
		"fieldRef": {WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "N", FieldRef: &FieldRef{FieldPath: "spec.nodeName"}}}},
			"env[N].fieldRef: not allowed under the restricted profile: the Downward API"},
		"unpinned image": {WorkloadSpec{Image: "ghcr.io/acme/api"}, "must be pinned"},
		"bare image":     {WorkloadSpec{Image: "api:v1"}, "registry host"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustFail(t, c.spec.Validate(ProfileRestricted), c.want)
			// The same spec is fine under Full (after its kind-structural
			// needs are met) — the refusal is the PROFILE's, not a
			// structural rule wearing a profile's name.
			if err := c.spec.Validate(ProfileFull); err != nil {
				t.Errorf("Full refused a spec only Restricted should refuse: %v", err)
			}
		})
	}
}

// TestFieldProfilesClassifyEverySpecField is the DEFAULT-DENY guarantee. It
// walks WorkloadSpec by reflection, descending into every field some
// restricted profile allows (so the EnvVar channels and Port/Probes fields
// are reached), and fails when a JSON path has no FieldProfiles entry. A
// field added to the spec without a classification therefore fails the
// build instead of silently becoming writable by a hosted user.
//
// It also fails on STALE entries (a path the spec no longer has) and on a
// Full-only entry with no reason, since the reason is the error text.
func TestFieldProfilesClassifyEverySpecField(t *testing.T) {
	stop := func(key string) bool {
		fp, ok := FieldProfiles[key]
		return !ok || !classifiesChildren(fp)
	}
	paths := specFieldPaths(reflect.TypeOf(WorkloadSpec{}), "", stop)
	sort.Strings(paths)
	have := map[string]bool{}
	for _, p := range paths {
		have[p] = true
		fp, ok := FieldProfiles[p]
		if !ok {
			t.Errorf("WorkloadSpec field %q has no FieldProfiles entry: classify it (default-deny — an unclassified field is refused by every profile)", p)
			continue
		}
		if fp == ProfileFull && fullOnlyReasons[p] == "" {
			t.Errorf("%q is Full-only but has no fullOnlyReasons entry; the refusal must say why", p)
		}
	}
	for key := range FieldProfiles {
		if !have[key] {
			t.Errorf("FieldProfiles has %q, which is not a WorkloadSpec path: delete the stale entry", key)
		}
	}
	for key := range fullOnlyReasons {
		if FieldProfiles[key] != ProfileFull {
			t.Errorf("fullOnlyReasons has %q, which is not a Full-only field", key)
		}
	}
	for k := range KindProfiles {
		if KindProfiles[k] == ProfileFull && fullOnlyKindReasons[k] == "" {
			t.Errorf("kind %q is Full-only with no reason", k)
		}
	}
	if len(KindProfiles) != 6 {
		t.Errorf("KindProfiles classifies %d kinds; every WorkloadKind needs an entry", len(KindProfiles))
	}
	// The ADR §3 Restricted kind set, pinned.
	var restrictedKinds []string
	for k, p := range KindProfiles {
		if p == ProfileRestricted {
			restrictedKinds = append(restrictedKinds, string(k))
		}
	}
	sort.Strings(restrictedKinds)
	if strings.Join(restrictedKinds, ",") != "job,service,worker" {
		t.Errorf("Restricted kinds = %v, want job, service, worker (ADR 0002 §3)", restrictedKinds)
	}
}

// TestUnclassifiedFieldIsRefused proves the walker itself is default-deny,
// not only the test above: a path missing from the table is refused at
// Validate time under Restricted, naming the field.
func TestUnclassifiedFieldIsRefused(t *testing.T) {
	saved := FieldProfiles["env.value"]
	delete(FieldProfiles, "env.value")
	defer func() { FieldProfiles["env.value"] = saved }()
	s := WorkloadSpec{Image: pinnedImage, Env: []EnvVar{{Name: "A", Value: "1"}}}
	mustFail(t, s.Validate(ProfileRestricted), "env[A].value: this field has no profile classification")
	if err := s.Validate(ProfileFull); err == nil || !strings.Contains(err.Error(), "no profile classification") {
		t.Errorf("an unclassified field must be refused under every profile, including Full: %v", err)
	}
}

// TestProbesDefaultsAndLivenessDerivation pins the readiness defaults and
// the derived liveness timings, which renderers take from Liveness() rather
// than restating the arithmetic.
func TestProbesDefaultsAndLivenessDerivation(t *testing.T) {
	d := Probes{}.WithDefaults()
	if d.InitialDelaySeconds != 0 || d.PeriodSeconds != 5 || d.TimeoutSeconds != 3 || d.FailureThreshold != 3 {
		t.Fatalf("readiness defaults = %+v, want 0/5/3/3", d)
	}
	if d.ReadinessPath != "/readyz" || d.LivenessPath != "/healthz" {
		t.Errorf("HTTP paths = %q/%q, want /readyz and /healthz", d.ReadinessPath, d.LivenessPath)
	}
	if tcp := (Probes{TCP: true}).WithDefaults(); tcp.ReadinessPath != "" || tcp.LivenessPath != "" {
		t.Errorf("a TCP probe must not gain paths: %+v", tcp)
	}
	// The timeout must exceed serverkit's 2s readiness deadline, or the
	// probe races the handler.
	if DefaultProbeTimeoutSeconds <= 2 {
		t.Errorf("DefaultProbeTimeoutSeconds = %d, must be > serverkit's 2s readiness deadline", DefaultProbeTimeoutSeconds)
	}

	if got, want := (Probes{}).Liveness(), (ProbeTimings{InitialDelaySeconds: 0 + 5*3 + 15, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 3}); got != want {
		t.Errorf("default Liveness() = %+v, want %+v", got, want)
	}
	tuned := Probes{InitialDelaySeconds: 10, PeriodSeconds: 7, TimeoutSeconds: 4, FailureThreshold: 5}
	if got, want := tuned.Liveness(), (ProbeTimings{InitialDelaySeconds: 10 + 7*5 + 15, PeriodSeconds: 14, TimeoutSeconds: 4, FailureThreshold: 5}); got != want {
		t.Errorf("tuned Liveness() = %+v, want %+v", got, want)
	}
	if got := tuned.Readiness(); got != (ProbeTimings{10, 7, 4, 5}) {
		t.Errorf("Readiness() = %+v, want the declared timings", got)
	}
	// Liveness can never fire before readiness has had its whole budget.
	for _, p := range []Probes{{}, tuned, {PeriodSeconds: 1, FailureThreshold: 1}} {
		r, l := p.Readiness(), p.Liveness()
		if l.InitialDelaySeconds <= r.InitialDelaySeconds+r.PeriodSeconds*r.FailureThreshold {
			t.Errorf("%+v: liveness initial %d does not outlast the readiness budget", p, l.InitialDelaySeconds)
		}
	}
}

// TestEffectiveProbesPolicy is ADR 0002 §5: declared probes as declared, TCP
// on the first port for a service with ports, none for anything else.
func TestEffectiveProbesPolicy(t *testing.T) {
	if p := (WorkloadSpec{Ports: []Port{{Name: "grpc", Port: 9090}}}).EffectiveProbes(); p == nil || !p.TCP || p.Port != 9090 || p.PeriodSeconds != 5 {
		t.Errorf("service with ports, no probes = %+v, want TCP on 9090 with default timings", p)
	}
	if p := (WorkloadSpec{}).EffectiveProbes(); p != nil {
		t.Errorf("service with no ports = %+v, want none (nothing to probe)", p)
	}
	if p := (WorkloadSpec{Kind: KindWorker, Ports: []Port{{Name: "m", Port: 9090}}}).EffectiveProbes(); p != nil {
		t.Errorf("worker with no declared probes = %+v, want none", p)
	}
	if p := (WorkloadSpec{Kind: KindJob}).EffectiveProbes(); p != nil {
		t.Errorf("job = %+v, want none", p)
	}
	// An operator may declare probes (its manager serves /healthz +
	// /readyz); none are invented for it.
	if p := (WorkloadSpec{Kind: KindOperator, Ports: []Port{{Name: "http", Port: 9191}}}).EffectiveProbes(); p != nil {
		t.Errorf("operator with no declared probes = %+v, want none", p)
	}
	op := WorkloadSpec{Kind: KindOperator, Image: pinnedImage, CRDs: []string{"Widget"}, Ports: []Port{{Name: "http", Port: 9191}}, Probes: &Probes{}}
	if p := op.EffectiveProbes(); p == nil || p.Port != 9191 || p.ReadinessPath != "/readyz" {
		t.Errorf("operator declared probe = %+v, want /readyz on 9191", p)
	}
	if err := op.Validate(ProfileFull); err != nil {
		t.Errorf("an operator declaring probes must validate under Full: %v", err)
	}
	// Declared: the port resolves to the port NAMED http, not the first.
	s := WorkloadSpec{Ports: []Port{{Name: "metrics", Port: 9090}, {Name: "http", Port: 8080}}, Probes: &Probes{}}
	if p := s.EffectiveProbes(); p == nil || p.TCP || p.Port != 8080 || p.ReadinessPath != "/readyz" || p.LivenessPath != "/healthz" {
		t.Errorf("declared HTTP probe = %+v, want /readyz + /healthz on the http port 8080", p)
	}
	s.Probes.Port = 9999
	if p := s.EffectiveProbes(); p.Port != 9999 {
		t.Errorf("explicit probes.port = %d, want 9999", p.Port)
	}
}

func TestWorkloadDefaultsAndEffectiveHelpers(t *testing.T) {
	d := WorkloadSpec{Ports: []Port{{Name: "http", Port: 80}}, Probes: &Probes{}}.WithDefaults()
	if d.Kind != KindService || d.Replicas != 1 || d.Ports[0].Protocol != ProtocolTCP || d.Resources.CPURequestMillicores != DefaultCPUMillicores || d.Probes.PeriodSeconds != 5 {
		t.Errorf("WithDefaults = %+v", d)
	}
	// WithDefaults must not write through to the caller's slices/pointers.
	in := WorkloadSpec{Ports: []Port{{Name: "http", Port: 80}}, Probes: &Probes{}}
	_ = in.WithDefaults()
	if in.Ports[0].Protocol != "" || in.Probes.PeriodSeconds != 0 {
		t.Errorf("WithDefaults mutated its receiver's shared memory: %+v %+v", in.Ports, in.Probes)
	}
	if got := (WorkloadSpec{Kind: KindJob}).EffectiveDeployPhase(); got != DeployPhasePreRollout {
		t.Errorf("standalone job phase = %q, want pre-rollout", got)
	}
	if got := (WorkloadSpec{Kind: KindJob, Before: []string{"api"}}).EffectiveDeployPhase(); got != "" {
		t.Errorf("gating job phase = %q, want none (it is an initContainer)", got)
	}
	if got := (WorkloadSpec{}).EffectiveDeployPhase(); got != "" {
		t.Errorf("service phase = %q, want none", got)
	}
	if !(WorkloadSpec{Kind: KindOperator}).EffectiveLeaderElection() || (WorkloadSpec{Kind: KindOperator, LeaderElection: ptr(false)}).EffectiveLeaderElection() {
		t.Error("operator leader election must default on and honour an explicit false")
	}
	if (WorkloadSpec{LeaderElection: ptr(true)}).EffectiveLeaderElection() {
		t.Error("a service never elects a leader")
	}
	if p := svc().ExposedPort(); p == nil || p.Port != 8080 {
		t.Errorf("ExposedPort = %+v", p)
	}
	if (WorkloadSpec{Ports: []Port{{Name: "http", Port: 80}}}).ExposedPort() != nil {
		t.Error("an unexposed port is not the exposed port")
	}
}

// podSpec is a service with every Full-only pod-level field set validly.
func podSpec() WorkloadSpec {
	s := svc()
	s.Ports[0].AppProtocol = "h2c"
	s.Ports[0].Domains = []string{"api.acme.com", "www.acme.com"}
	s.Sidecars = []Container{{Name: "cloud-sql-proxy", Image: "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1", Args: []string{"--port=5432"}}}
	s.Volumes = []Volume{
		{Name: "scratch", MountPath: "/scratch", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}},
		{Name: "key", MountPath: "/etc/key", ReadOnly: true, Source: VolumeSource{Secret: &SecretVolumeSource{Name: "k"}}},
	}
	s.ServiceAccount = "reliant-cloudsql"
	s.NodeSelector = map[string]string{"cloud.google.com/gke-nodepool": "general"}
	s.Tolerations = []Toleration{{Key: "pool", Value: "general", Effect: TaintNoSchedule}}
	s.PodAnnotations = map[string]string{"cluster-autoscaler.kubernetes.io/safe-to-evict": "true"}
	return s
}

// TestWorkloadPodLevelFields: the Full-only pod fields validate under Full,
// and every structural rule on them holds under BOTH profiles.
func TestWorkloadPodLevelFields(t *testing.T) {
	mustPass(t, podSpec(), ProfileFull)
	// "data" and /data are free for a stateless workload's own volume.
	stateless := WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "data", MountPath: "/data", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}}
	mustPass(t, stateless, ProfileFull)

	vol := func(src VolumeSource) WorkloadSpec {
		return WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "v", MountPath: "/v", Source: src}}}
	}
	withSA := func(mut func(*WorkloadSpec)) WorkloadSpec {
		s := WorkloadSpec{Image: pinnedImage, ServiceAccount: "existing"}
		mut(&s)
		return s
	}
	rule := []PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}}
	cases := map[string]struct {
		spec WorkloadSpec
		want string
	}{
		// volume source: exactly one
		"volume source none": {vol(VolumeSource{}), "exactly one of secret, configMap, emptyDir, pvc (got 0)"},
		"volume source two": {vol(VolumeSource{EmptyDir: &EmptyDirVolumeSource{}, PVC: &PVCVolumeSource{ClaimName: "c"}}),
			"exactly one of secret, configMap, emptyDir, pvc (got 2)"},
		"volume source four": {vol(VolumeSource{EmptyDir: &EmptyDirVolumeSource{}, PVC: &PVCVolumeSource{ClaimName: "c"}, Secret: &SecretVolumeSource{Name: "s"}, ConfigMap: &ConfigMapVolumeSource{Name: "c"}}),
			"(got 4)"},
		"secret needs a name": {vol(VolumeSource{Secret: &SecretVolumeSource{}}), "source.secret.name is required"},
		"pvc needs a claim":   {vol(VolumeSource{PVC: &PVCVolumeSource{}}), "source.pvc.claimName is required"},
		"negative sizeLimit":  {vol(VolumeSource{EmptyDir: &EmptyDirVolumeSource{SizeLimitBytes: -1}}), "sizeLimitBytes must not be negative"},
		"item path escapes":   {vol(VolumeSource{Secret: &SecretVolumeSource{Name: "s", Items: []KeyToPath{{Key: "k", Path: "../etc/passwd"}}}}), "must not contain '..'"},
		"item path absolute":  {vol(VolumeSource{ConfigMap: &ConfigMapVolumeSource{Name: "c", Items: []KeyToPath{{Key: "k", Path: "/abs"}}}}), "must be relative"},
		"item path twice":     {vol(VolumeSource{ConfigMap: &ConfigMapVolumeSource{Name: "c", Items: []KeyToPath{{Key: "a", Path: "x"}, {Key: "b", Path: "x"}}}}), "projected twice"},
		"relative mountPath":  {WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "v", MountPath: "v", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}}, "must be absolute"},
		"reserved tmp name":   {WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "tmp", MountPath: "/x", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}}, `"tmp" is reserved`},
		"reserved tmp path":   {WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "t", MountPath: "/tmp", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}}, "/tmp is already mounted by volume tmp"},
		"data reserved with storage": {WorkloadSpec{Image: pinnedImage, StorageGiB: 1, Volumes: []Volume{{Name: "data", MountPath: "/x", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}},
			`"data" is reserved`},
		"duplicate volume name": {WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "v", MountPath: "/a", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}, {Name: "v", MountPath: "/b", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}},
			"volume name is declared twice"},
		"duplicate mountPath": {WorkloadSpec{Image: pinnedImage, Volumes: []Volume{{Name: "a", MountPath: "/m", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}, {Name: "b", MountPath: "/m", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}},
			"already mounted by volume a"},
		"volumes not for tool": {WorkloadSpec{Kind: KindTool, Image: pinnedImage, Volumes: []Volume{{Name: "v", MountPath: "/v", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{}}}}},
			`volumes is not supported for kind "tool"`},

		// serviceAccount vs RBAC
		"serviceAccount + namespacedRBAC": {withSA(func(s *WorkloadSpec) { s.NamespacedRBAC = rule }), "serviceAccount \"existing\" and namespacedRBAC are mutually exclusive"},
		"serviceAccount + clusterRBAC": {withSA(func(s *WorkloadSpec) { s.Kind = KindOperator; s.CRDs = []string{"W"}; s.ClusterRBAC = rule }),
			"serviceAccount \"existing\" and clusterRBAC are mutually exclusive"},
		"serviceAccount + SA annotations": {withSA(func(s *WorkloadSpec) { s.ServiceAccountAnnotations = map[string]string{"a": "b"} }),
			"and serviceAccountAnnotations are mutually exclusive"},
		"serviceAccount not for tool": {WorkloadSpec{Kind: KindTool, Image: pinnedImage, ServiceAccount: "x"}, `serviceAccount is not supported for kind "tool"`},

		// custom domains
		"domains on unexposed port": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Domains: []string{"a.acme.com"}}}},
			"domains are only meaningful on the exposed port"},
		"domain not a hostname": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Expose: true, Domains: []string{"Not A Host"}}}},
			"not a lowercase DNS hostname"},
		"domain twice": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Expose: true, Domains: []string{"a.acme.com", "a.acme.com"}}}},
			"listed twice"},
		"too many domains": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, Expose: true, Domains: []string{"a.x.io", "b.x.io", "c.x.io", "d.x.io", "e.x.io", "f.x.io", "g.x.io", "h.x.io", "i.x.io"}}}},
			"at most 8"},
		"bad appProtocol": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 80, AppProtocol: "H2C!"}}}, "appProtocol"},

		// sidecars
		"sidecar needs image": {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s"}}}, "sidecars[s]: image is required"},
		"sidecar name twice":  {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s", Image: "i"}, {Name: "s", Image: "i"}}}, "sidecar name is declared twice"},
		"sidecar bad name":    {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "Proxy", Image: "i"}}}, "RFC-1123"},
		"sidecar port collides with main": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 8080}}, Sidecars: []Container{{Name: "s", Image: "i", Ports: []Port{{Name: "proxy", Port: 8080}}}}},
			"port 8080/tcp is declared by both the main container and s"},
		"sidecar port name collides": {WorkloadSpec{Image: pinnedImage, Ports: []Port{{Name: "http", Port: 8080}}, Sidecars: []Container{{Name: "s", Image: "i", Ports: []Port{{Name: "http", Port: 9000}}}}},
			`name "http" is declared by both`},
		"sidecar port exposed": {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s", Image: "i", Ports: []Port{{Name: "p", Port: 9000, Expose: true}}}}},
			"container ports only"},
		"sidecar probes need a port": {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s", Image: "i", Probes: &Probes{}}}}, "sidecars[s]: probes need a port"},
		"sidecar env two channels":   {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s", Image: "i", Env: []EnvVar{{Name: "X", Value: "1", ManagedSecret: &ManagedSecretRef{Name: "X"}}}}}}, "sidecars[s]: env var X sets more than one"},
		"sidecar limit below request": {WorkloadSpec{Image: pinnedImage, Sidecars: []Container{{Name: "s", Image: "i", Resources: Resources{CPURequestMillicores: 100, CPULimitMillicores: 50}}}},
			"sidecars[s]: resources.cpuLimitMillicores"},

		// placement
		"nodeSelector bad value": {WorkloadSpec{Image: pinnedImage, NodeSelector: map[string]string{"pool": "not valid!"}}, "not a valid label value"},
		"nodeSelector bad key":   {WorkloadSpec{Image: pinnedImage, NodeSelector: map[string]string{"bad key": "x"}}, "not a valid label key"},
		"toleration empty key needs Exists": {WorkloadSpec{Image: pinnedImage, Tolerations: []Toleration{{Effect: TaintNoSchedule}}},
			"requires operator Exists"},
		"toleration Exists with value": {WorkloadSpec{Image: pinnedImage, Tolerations: []Toleration{{Key: "k", Operator: TolerationOpExists, Value: "v"}}},
			"value must be empty"},
		"tolerationSeconds without NoExecute": {WorkloadSpec{Image: pinnedImage, Tolerations: []Toleration{{Key: "k", Effect: TaintNoSchedule, TolerationSeconds: ptr(int64(5))}}},
			"only applies to effect NoExecute"},
		"toleration bad effect":  {WorkloadSpec{Image: pinnedImage, Tolerations: []Toleration{{Key: "k", Effect: "Evict"}}}, "effect \"Evict\""},
		"podAnnotations bad key": {WorkloadSpec{Image: pinnedImage, PodAnnotations: map[string]string{"no spaces": "x"}}, "podAnnotations key"},
	}
	for name, c := range cases {
		for _, p := range []Profile{ProfileFull, ProfileRestricted} {
			t.Run(name+"/"+p.String(), func(t *testing.T) {
				mustFail(t, c.spec.Validate(p), c.want)
			})
		}
	}
}

// TestWorkloadPodLevelProfiles: every new pod-level field is refused under
// Restricted by name, and appProtocol and domains are allowed there.
func TestWorkloadPodLevelProfiles(t *testing.T) {
	restrictedOK := svc()
	restrictedOK.Ports[0].AppProtocol = "grpc"
	restrictedOK.Ports[0].Domains = []string{"api.acme.com"}
	mustPass(t, restrictedOK, ProfileRestricted)

	err := podSpec().Validate(ProfileRestricted)
	for field, reason := range map[string]string{
		"sidecars":       "single-container policy",
		"volumes":        "use storageGiB for a persistent disk",
		"serviceAccount": "borrow its identity",
		"nodeSelector":   "hosted placement",
		"tolerations":    "hosted placement",
		"podAnnotations": "platform integrations",
	} {
		mustFail(t, err, field+": not allowed under the restricted profile: ")
		mustFail(t, err, reason)
		if FieldProfiles[field] != ProfileFull {
			t.Errorf("FieldProfiles[%q] = %s, want full", field, FieldProfiles[field])
		}
	}
	for _, field := range []string{"ports.appProtocol", "ports.domains"} {
		if FieldProfiles[field] != ProfileRestricted {
			t.Errorf("FieldProfiles[%q] = %s, want restricted", field, FieldProfiles[field])
		}
	}
	if strings.Contains(err.Error(), "appProtocol") || strings.Contains(err.Error(), "domains") {
		t.Errorf("Restricted refused a Restricted-allowed port field: %v", err)
	}
}

func TestPodLevelHelpers(t *testing.T) {
	if (WorkloadSpec{ServiceAccount: "x"}).UsesGeneratedServiceAccount() || !(WorkloadSpec{}).UsesGeneratedServiceAccount() {
		t.Error("UsesGeneratedServiceAccount must be false exactly when serviceAccount names an existing one")
	}
	c := Container{Ports: []Port{{Name: "admin", Port: 9000}, {Name: "http", Port: 9801}}, Probes: &Probes{TCP: true}}
	if p := c.EffectiveProbes(); p == nil || p.Port != 9801 || p.PeriodSeconds != DefaultProbePeriodSeconds {
		t.Errorf("sidecar EffectiveProbes = %+v, want port http=9801 with defaults", p)
	}
	if (Container{Ports: []Port{{Name: "http", Port: 1}}}).EffectiveProbes() != nil {
		t.Error("a sidecar with no declared probes gets none (no implicit TCP probe)")
	}
	in := WorkloadSpec{Sidecars: []Container{{Name: "s", Ports: []Port{{Name: "p", Port: 1}}, Probes: &Probes{}}}}
	d := in.WithDefaults()
	if s := d.Sidecars[0]; s.Resources.CPURequestMillicores != DefaultCPUMillicores || s.Ports[0].Protocol != ProtocolTCP || s.Probes.PeriodSeconds != 5 {
		t.Errorf("sidecar defaults = %+v", s)
	}
	if in.Sidecars[0].Ports[0].Protocol != "" || in.Sidecars[0].Probes.PeriodSeconds != 0 || in.Sidecars[0].Resources.CPURequestMillicores != 0 {
		t.Error("WithDefaults mutated a sidecar through shared memory")
	}
	if p := podSpec().ExposedPort(); p == nil || len(p.Domains) != 2 {
		t.Errorf("ExposedPort().Domains = %+v", p)
	}
}
