package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/deploy"
	v1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// fakeClaimableControlPlane is the hosted-deploy fake plus the ClaimableService
// as the design's §11 wire contract specifies it, and GetTenant (the org the
// anonymous token acts for). Every deploy rpc is checked to arrive carrying
// the anonymous token, never anything else.
type fakeClaimableControlPlane struct {
	*fakeDeployService

	mu         sync.Mutex
	starts     int
	challenges int
	// throttle, when set, makes StartClaimableDeploy answer ResourceExhausted.
	throttle *struct {
		scope   string
		seconds int64
	}
	// claimed makes every authenticated call answer access_token_revoked.
	claimed bool
	// token is what Start hands out.
	token string
	// authSeen is the Authorization header of every non-claimable rpc.
	authSeen []string
	// startedWithAuth is true if StartClaimableDeploy ever carried a bearer.
	startedWithAuth bool
	// availability is GetClaimableAvailability's answer; nil is "available".
	availability      *cloud.ClaimableAvailability
	availabilityCalls int
}

func (f *fakeClaimableControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/" + cloud.ProcGetClaimableAvailability:
		f.mu.Lock()
		f.availabilityCalls++
		av := cloud.ClaimableAvailability{Available: true, ComputeAvailable: true}
		if f.availability != nil {
			av = *f.availability
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(av)
		return
	case "/" + cloud.ProcGetClaimableChallenge:
		f.mu.Lock()
		f.challenges++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"challenge":"signed-challenge","difficultyBits":8,"expiresAt":"2030-01-01T00:00:00Z"}`))
		return
	case "/" + cloud.ProcStartClaimableDeploy:
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			f.startedWithAuth = true
		}
		var req cloud.ClaimableStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if f.throttle != nil {
			w.Header().Set(cloud.ReasonHeader, cloud.ReasonClaimableThrottled)
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "resource_exhausted", "message": "throttled",
				"details": []map[string]any{{
					"type": "controlplane.v1.ClaimableThrottle", "value": "AA",
					"debug": map[string]any{"retryAfterSeconds": protoInt64(f.throttle.seconds), "scope": f.throttle.scope},
				}},
			})
			return
		}
		if req.Project != "acme" || req.Env != "hosted" || req.DeviceID == "" || req.PoWChallenge != "signed-challenge" || req.PoWNonce == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "invalid_argument", "message": "bad start request"})
			return
		}
		f.starts++
		_, _ = w.Write([]byte(`{"accessToken":"` + f.token + `","orgId":"org-anon","claimUrl":"https://app.reliant.dev/claim#rcl_9f3KQe",` +
			`"expiresAt":"2026-10-29T14:02:00Z","purgeAfter":"2026-11-05T14:02:00Z","reused":false}`))
		return
	}
	f.mu.Lock()
	f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
	claimed := f.claimed
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		connectErr(w, http.StatusUnauthorized, "unauthenticated", "wrong credential")
		return
	}
	if claimed {
		w.Header().Set(cloud.ReasonHeader, cloud.ReasonAccessTokenRevoked)
		connectErr(w, http.StatusUnauthorized, "unauthenticated", "revoked")
		return
	}
	if r.URL.Path == "/"+cloud.ProcGetOrganization {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cloud.OrganizationReply("4f3c2b1a-0000-4000-8000-0000000000aa"))
		return
	}
	f.fakeDeployService.ServeHTTP(w, r)
}

// protoInt64 spells an int64 as protojson does: a decimal string.
func protoInt64(n int64) string { return strconv.FormatInt(n, 10) }

// claimableFixture is a one-static-site hosted project, the fake control plane
// and every seam the hosted-static e2e test stubs.
func claimableFixture(t *testing.T) (*fakeClaimableControlPlane, string) {
	t.Helper()
	fake := &fakeClaimableControlPlane{fakeDeployService: newFakeDeployService(map[string]string{}), token: "rlat_anonymousToken0123"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := writeHostedStaticProject(t, srv.URL)
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "")
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "")
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	prevPoll := hostedPollInterval
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = prevPoll })
	stubHostedBundleRegistry(t, fake.fakeDeployService)
	prevPush := hostedStaticPusher
	hostedStaticPusher = func(context.Context, string, string, deploytarget.StaticSiteFrontend) (string, error) {
		return hostedStaticDigest, nil
	}
	t.Cleanup(func() { hostedStaticPusher = prevPush })
	prevNow := claimableNow
	claimableNow = func() time.Time { return time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC) }
	t.Cleanup(func() { claimableNow = prevNow })
	return fake, srv.URL
}

func decodeClaimDoc(t *testing.T, out string) map[string]any {
	t.Helper()
	// The cut may print before the document in a captured run; the JSON
	// document is the last top-level object on stdout.
	i := strings.LastIndex(out, "\n{\n")
	if i < 0 && strings.HasPrefix(out, "{") {
		i = -1
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out[i+1:]), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	return doc
}

// TestClaimableDeploy_PublishesAndPrintsTheClaim is the design's acceptance
// path: no credential plus --claimable publishes, exits 0, prints the claim
// block, and a redeploy reuses the stored entry.
func TestClaimableDeploy_PublishesAndPrintsTheClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
	if err != nil {
		t.Fatalf("a live unclaimed deploy must exit 0: %v\n%s", err, out)
	}
	doc := decodeClaimDoc(t, out)
	if doc["ok"] != true || doc["exit_code"] != float64(0) {
		t.Fatalf("envelope = ok:%v exit_code:%v", doc["ok"], doc["exit_code"])
	}
	claim, _ := doc["claim"].(map[string]any)
	if claim == nil {
		t.Fatalf("--json carries no `claim` object:\n%s", out)
	}
	for k, want := range map[string]string{
		"state": "live", "claim_url": "https://app.reliant.dev/claim#rcl_9f3KQe",
		"expires_at": "2026-10-29T14:02:00Z", "purge_after": "2026-11-05T14:02:00Z",
	} {
		if claim[k] != want {
			t.Errorf("claim.%s = %v, want %q", k, claim[k], want)
		}
	}
	if u, _ := claim["url"].(string); !strings.HasPrefix(u, "https://") {
		t.Errorf("claim.url = %q, want the deploy's live address", u)
	}
	instr, _ := claim["agent_instructions"].(string)
	for _, want := range []string{"Give claim_url to your user", "whoever claims it first owns the deploy", "env status hosted --claimable"} {
		if !strings.Contains(instr, want) {
			t.Errorf("agent_instructions lacks %q:\n%s", want, instr)
		}
	}
	if fake.starts != 1 || fake.challenges != 1 || fake.startedWithAuth {
		t.Fatalf("starts=%d challenges=%d startedWithAuth=%v, want one authenticated-free start", fake.starts, fake.challenges, fake.startedWithAuth)
	}
	// Every deploy rpc carried the anonymous token and nothing else.
	for _, a := range fake.authSeen {
		if a != "Bearer "+fake.token {
			t.Fatalf("a deploy rpc carried %q, want the anonymous token", a)
		}
	}
	if len(fake.authSeen) == 0 {
		t.Fatal("the deploy made no authenticated calls")
	}
	if fake.bundles[fake.envs["hosted"]]["web"] == nil {
		t.Fatal("the static site was not published")
	}

	// A redeploy reuses the stored entry: no new challenge, no new start.
	out, err = runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
	if err != nil {
		t.Fatalf("redeploy: %v\n%s", err, out)
	}
	if fake.starts != 1 || fake.challenges != 1 {
		t.Fatalf("a redeploy minted again: starts=%d challenges=%d", fake.starts, fake.challenges)
	}

	// The stored claim is printable again with no credential, and the stored
	// token never resolves as a login.
	statusOut, err := runForge(t, "env", "status", "hosted", "--claimable")
	if err != nil {
		t.Fatalf("status --claimable: %v\n%s", err, statusOut)
	}
	if !strings.Contains(statusOut, "Claim:  https://app.reliant.dev/claim#rcl_9f3KQe") {
		t.Errorf("status --claimable lacks the claim link:\n%s", statusOut)
	}
}

// The text output is the design's block, in order.
func TestPrintClaimBlock_TextMatchesTheDesign(t *testing.T) {
	prev := claimableNow
	claimableNow = func() time.Time { return time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC) }
	defer func() { claimableNow = prev }()
	var b strings.Builder
	printClaimBlock(&b, "prod", &deployClaimJSON{
		State: "live", URL: "https://amber-quokka.reliantpreview.dev", ClaimURL: "https://app.reliant.dev/claim#rcl_9f3KQe",
		ExpiresAt: time.Date(2026, 10, 29, 14, 2, 0, 0, time.UTC), PurgeAfter: time.Date(2026, 11, 5, 14, 2, 0, 0, time.UTC),
	})
	for _, want := range []string{
		"✓ Live:   https://amber-quokka.reliantpreview.dev",
		"This deploy belongs to no account. It goes offline 2026-10-29 14:02 UTC (in 72h)",
		"and is deleted 7 days after that, unless someone claims it.",
		"Claim:  https://app.reliant.dev/claim#rcl_9f3KQe",
		"FOR AN AGENT: give the claim link above to your user and tell them it expires in 72h.",
		"Opening it signs them up for free and makes this site theirs.",
		"Treat the link like a password: whoever claims it first owns the site. Do not",
		"env status prod --claimable",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("claim block lacks %q:\n%s", want, b.String())
		}
	}
}

// An unclaimed BACKEND has no outbound internet; a static site makes no
// outbound calls, so it is told nothing. The notice is in the text block, in
// the instructions an agent reads from --json, and as a machine-readable flag.
func TestClaimDocument_NoOutboundInternetOnlyForCompute(t *testing.T) {
	now := time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC)
	cred := credentials.Credential{Claimable: &credentials.Claimable{
		URL: "https://amber-quokka.reliantpreview.dev", ClaimURL: "https://app.reliant.dev/claim#rcl_9f3KQe",
		ExpiresAt: now.Add(72 * time.Hour), PurgeAfter: now.Add(240 * time.Hour),
	}}
	backend := claimDocument("prod", cred, now, true)
	if !backend.NoOutboundInternet {
		t.Error("a service's claim document does not carry no_outbound_internet")
	}
	for _, want := range []string{"no outbound internet", "fails with a connection error", "not a bug in the app", "only the cluster's own names", "restarts the backend on the normal hosted network"} {
		if !strings.Contains(backend.AgentInstructions, want) {
			t.Errorf("agent_instructions lacks %q:\n%s", want, backend.AgentInstructions)
		}
	}
	var text strings.Builder
	claimableNow = func() time.Time { return now }
	defer func() { claimableNow = time.Now }()
	printClaimBlock(&text, "prod", backend)
	for _, want := range []string{"NO OUTBOUND INTERNET until claimed", "uses its own", "not a bug in the app", "restarts"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("claim block lacks %q:\n%s", want, text.String())
		}
	}

	static := claimDocument("prod", cred, now, false)
	if static.NoOutboundInternet || strings.Contains(static.AgentInstructions, "outbound") {
		t.Errorf("a static site was told it has no outbound internet: %+v", static)
	}
	text.Reset()
	printClaimBlock(&text, "prod", static)
	if strings.Contains(strings.ToLower(text.String()), "outbound") {
		t.Errorf("a static site's claim block mentions outbound internet:\n%s", text.String())
	}
	b, _ := json.Marshal(static)
	if strings.Contains(string(b), "no_outbound_internet") {
		t.Errorf("the flag is serialised for a static site: %s", b)
	}
}

func TestClaimableDeploy_ThrottleExits4WithTheRetryBlockAndNeverRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	fake.throttle = &struct {
		scope   string
		seconds int64
	}{"platform", 840}

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--json")
	if err == nil {
		t.Fatalf("a throttle must fail:\n%s", out)
	}
	if code := exitCodeForError(err); code != exitRefused {
		t.Fatalf("exit code = %d, want 4", code)
	}
	for _, want := range []string{
		"Claimable deploys are throttled right now: too many new anonymous accounts",
		"(platform-wide limit). Nothing was created.",
		"retry after  14m  (at 2026-10-26T14:16:00Z)",
		"env deploy hosted --claimable",
		"forge login, then",
		"(exit 4: throttled, not a failure of your project)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("throttle block lacks %q:\n%s", want, err)
		}
	}
	doc := decodeClaimDoc(t, out)
	if doc["ok"] != false || doc["exit_code"] != float64(4) || doc["error_code"] != "resource_exhausted" ||
		doc["error_reason"] != "claimable_throttled" || doc["retry_after_seconds"] != float64(840) ||
		doc["retry_at"] != "2026-10-26T14:16:00Z" || doc["throttle_scope"] != "platform" {
		t.Fatalf("json facts = %v", doc)
	}
	if _, has := doc["claim"]; has {
		t.Error("a throttled start must not carry a claim")
	}
	if fake.challenges != 1 {
		t.Errorf("forge asked for %d challenges; a throttle must never be retried", fake.challenges)
	}
	// Nothing was stored, so nothing can later pass for a deploy.
	if _, err := cloud.LoadClaimable(cloud.Endpoint{URL: fakeURLOf(t)}, "acme", "hosted", claimableNow()); err == nil {
		t.Error("a throttled start stored a credential")
	}
}

func fakeURLOf(t *testing.T) string {
	t.Helper()
	ep, err := RenderKCL(context.Background(), projectDirForKCL(), "hosted")
	if err != nil {
		t.Fatal(err)
	}
	return ep.ControlPlane.Endpoint
}

func TestNewClaimableThrottle_ScopesReadAsTheDesignSays(t *testing.T) {
	prev := claimableNow
	claimableNow = func() time.Time { return time.Date(2026, 10, 26, 14, 48, 0, 0, time.UTC) }
	defer func() { claimableNow = prev }()
	for scope, want := range map[string]string{
		"network":  "too many claimable deploys from your network",
		"device":   "this machine already has 3 live claimable deploys",
		"platform": "too many new anonymous accounts",
	} {
		err := newClaimableThrottle("prod", cloud.ClaimableThrottle{Scope: scope, RetryAfter: 14 * time.Minute})
		if !strings.Contains(err.Error(), want) {
			t.Errorf("scope %s: %q lacks %q", scope, err, want)
		}
		if got := exitCodeForError(err); got != exitRefused {
			t.Errorf("scope %s exit = %d", scope, got)
		}
		facts := errorFactsOf(err)
		if facts.ThrottleScope != scope || facts.RetryAfterSeconds != 840 || facts.ErrorReason != "claimable_throttled" ||
			facts.RetryAt != "2026-10-26T15:02:00Z" {
			t.Errorf("scope %s facts = %+v", scope, facts)
		}
	}
}

func TestFormatRetryAfter(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s", 14 * time.Minute: "14m", 14*time.Minute + time.Second: "15m",
		time.Hour: "1h", 65 * time.Minute: "1h5m",
	} {
		if got := formatRetryAfter(d); got != want {
			t.Errorf("formatRetryAfter(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestClaimableDeploy_SignedInIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	t.Setenv("ACME_CP_TOKEN", "rlat_real_login")

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--json")
	if err == nil || exitCodeForError(err) != 1 {
		t.Fatalf("want exit 1; err=%v code=%d\n%s", err, exitCodeForError(err), out)
	}
	if !strings.Contains(err.Error(), "you are signed in to") || !strings.Contains(err.Error(), "drop --claimable to deploy to your organization") {
		t.Errorf("refusal text:\n%s", err)
	}
	if fake.challenges != 0 || fake.starts != 0 {
		t.Errorf("a signed-in --claimable still minted an anonymous account (challenges=%d starts=%d)", fake.challenges, fake.starts)
	}
	if strings.Contains(err.Error(), "rlat_real_login") {
		t.Error("the refusal printed the credential")
	}
}

func TestClaimableDeploy_ClaimedDeployExits4(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	if out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json"); err != nil {
		t.Fatalf("first deploy: %v\n%s", err, out)
	}
	fake.claimed = true
	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
	if err == nil || exitCodeForError(err) != exitRefused {
		t.Fatalf("a claimed deploy must exit 4; err=%v\n%s", err, out)
	}
	if !strings.Contains(err.Error(), "this deploy was claimed") || !strings.Contains(err.Error(), "run forge login and deploy without --claimable") {
		t.Errorf("claimed text:\n%s", err)
	}
	if doc := decodeClaimDoc(t, out); doc["error_reason"] != "claimable_claimed" || doc["exit_code"] != float64(4) {
		t.Errorf("json facts = %v", doc)
	}
	if fake.starts != 1 {
		t.Errorf("a claimed deploy was re-minted (starts=%d)", fake.starts)
	}
}

func TestClaimableIneligibility(t *testing.T) {
	t.Parallel()
	svc := func(name, kind string) WorkloadEntity {
		w := WorkloadEntity{Name: name, Runtime: RuntimeEntity{Type: RuntimeHosted}}
		w.Spec.Kind = v1alpha1.WorkloadKind(kind)
		w.Spec.Resources = v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 << 20}
		return w
	}
	gating := func(name string) WorkloadEntity {
		w := svc(name, "job")
		w.Spec.Before = []string{v1alpha1.BeforeAll}
		return w
	}
	postRollout := func(name string) WorkloadEntity {
		w := svc(name, "job")
		w.Spec.DeployPhase = v1alpha1.DeployPhasePostRollout
		return w
	}
	small := func(name string) DatabaseEntity {
		return DatabaseEntity{Name: name, Runtime: RuntimeHosted, Spec: v1alpha1.ManagedDatabaseSpec{StorageGiB: 1}}
	}
	cases := []struct {
		name string
		e    *KCLEntities
		want []string
	}{
		{"one service", &KCLEntities{Workloads: []WorkloadEntity{svc("api", "service")}}, nil},
		{"service and its pre-rollout migration", &KCLEntities{Workloads: []WorkloadEntity{svc("api", "service"), svc("migrate", "job")}}, nil},
		// The scaffold's migrate job gates every workload (`before =
		// [fw.BEFORE_ALL]`): it has no deployPhase of its own, and IS the
		// service's migration.
		{"service and its gating migration", &KCLEntities{Workloads: []WorkloadEntity{svc("api", "service"), gating("migrate")}}, nil},
		{"a post-rollout job", &KCLEntities{Workloads: []WorkloadEntity{svc("api", "service"), postRollout("seed")}},
			[]string{`job "seed" runs after the rollout`}},
		{"a worker", &KCLEntities{Workloads: []WorkloadEntity{svc("api", "service"), svc("w", "worker")}}, []string{"worker \"w\""}},
		{"two services", &KCLEntities{Workloads: []WorkloadEntity{svc("a", "service"), svc("b", "service")}}, []string{"2 services"}},
		{"a cron", &KCLEntities{Workloads: []WorkloadEntity{svc("c", "cron")}}, []string{"cron \"c\""}},
		{"two databases", &KCLEntities{Databases: []DatabaseEntity{small("a"), small("b")}}, []string{"2 databases"}},
		{"a 1 GiB database", &KCLEntities{Databases: []DatabaseEntity{small("db")}}, nil},
		// The scaffold default (10 GiB) is what an anonymous database may not
		// declare, and the refusal names the edit.
		{"a default 10 GiB database", &KCLEntities{Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted}}},
			[]string{`database "db" asks for 10 GiB of storage (at most 1 GiB); the platform provisions 256 MiB of data (1 GiB volume)`, "spec = {storageGiB = 1}"}},
		{"a database with a standby", &KCLEntities{Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted, Spec: v1alpha1.ManagedDatabaseSpec{StorageGiB: 1, Instances: 2}}}},
			[]string{`database "db" asks for 2 instances (1 is allowed)`}},
	}
	for _, c := range cases {
		got := strings.Join(claimableIneligibility(c.e, deploy.ClaimableLimitsFallback()), "|")
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s: unexpected reasons %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: reasons %q lack %q", c.name, got, w)
			}
		}
	}
	err := claimableIneligibleError("prod", deploy.ClaimableLimitsFallback(), []string{"worker \"w\" (only one service may run)"})
	if exitCodeForError(err) != exitRefused || !strings.Contains(err.Error(), "deploy it to your own account instead: forge login") {
		t.Errorf("ineligible error: code=%d %s", exitCodeForError(err), err)
	}
}

// The claimable caps (here forge's compiled-in copy, 125m / 128 MiB / one
// replica) are per workload with no burst, in control-plane's words
// (planlimits/claimable.go, computegate).
func TestClaimableIneligibilityChecksTheClaimableCaps(t *testing.T) {
	t.Parallel()
	svc := func(res v1alpha1.Resources, replicas int32) *KCLEntities {
		w := WorkloadEntity{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}
		w.Spec.Kind = v1alpha1.KindService
		w.Spec.Resources = res
		w.Spec.Replicas = replicas
		return &KCLEntities{Workloads: []WorkloadEntity{w}}
	}
	const mib = 1 << 20
	cases := []struct {
		name string
		e    *KCLEntities
		want []string
	}{
		{"125m / 128Mi passes", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 * mib}, 1), nil},
		{"unset replicas pass", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 * mib}, 0), nil},
		{"250m / 128Mi is refused", svc(v1alpha1.Resources{CPURequestMillicores: 250, MemoryRequestBytes: 128 * mib}, 1),
			[]string{`workload "api" asks for 250m of CPU (at most 125m, no burst)`}},
		{"125m / 256Mi is refused", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 256 * mib}, 1),
			[]string{`workload "api" asks for 256 MiB of memory (at most 128 MiB, no burst)`}},
		{"a limit above the cap is a burst", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 * mib, MemoryLimitBytes: 1024 * mib}, 1),
			[]string{"1024 MiB of memory"}},
		{"the entry shape (250m / 1Gi) is over the caps, with the exact fix", svc(v1alpha1.Resources{}, 1),
			[]string{"1024 MiB of memory (at most 128 MiB, no burst)",
				"resources = fw.Resources {cpuRequestMillicores = 125, cpuLimitMillicores = 125, memoryRequestBytes = forge.mib(128), memoryLimitBytes = forge.mib(128)}"}},
		// Off the hosted shape band (125m needs exactly 512 MiB there), and
		// inside the claimable caps: the band is not this plan's rule.
		{"125m / 64Mi passes: the shape band does not apply", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 64 * mib}, 1), nil},
		{"100m / 128Mi passes: no 125m granularity either", svc(v1alpha1.Resources{CPURequestMillicores: 100, MemoryRequestBytes: 128 * mib}, 1), nil},
		{"two replicas", svc(v1alpha1.Resources{CPURequestMillicores: 125, MemoryRequestBytes: 128 * mib}, 2),
			[]string{`workload "api" asks for 2 replicas (1 is allowed)`}},
	}
	for _, c := range cases {
		got := strings.Join(claimableIneligibility(c.e, deploy.ClaimableLimitsFallback()), "|")
		if len(c.want) == 0 && got != "" {
			t.Errorf("%s: unexpected reasons %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: reasons %q lack %q", c.name, got, w)
			}
		}
	}
	err := claimableIneligibleError("prod", deploy.ClaimableLimitsFallback(), claimableIneligibility(svc(v1alpha1.Resources{CPURequestMillicores: 500, MemoryRequestBytes: 256 * mib}, 1), deploy.ClaimableLimitsFallback()))
	if exitCodeForError(err) != exitRefused || !strings.Contains(err.Error(), "does not fit an anonymous deploy") {
		t.Errorf("ineligible error: code=%d %s", exitCodeForError(err), err)
	}
}

// The caps are the control plane's: an answer that publishes them is what
// every check judges by, and one that does not falls back to the compiled-in
// copy and says so.
func TestClaimableLimitsFromTheControlPlane(t *testing.T) {
	t.Parallel()
	ep := cloud.Endpoint{URL: "https://cp.example"}
	published := cloud.ClaimableLimits{MaxServices: 1, MaxReplicas: 1, MaxMigrationJobs: 1,
		WorkloadCPUMillicores: 250, WorkloadMemoryMib: 256, MaxDatabases: 1, DatabaseInstances: 1,
		DatabaseDeclaredStorageGib: 1, DatabaseStorageMib: 256, MaxStaticSites: 1}
	s := claimableLimitsFrom(ep, cloud.ClaimableAvailability{Limits: &published}, true, nil)
	if !s.published || s.limits.WorkloadMemoryBytes != 256<<20 || !strings.Contains(s.note(), "published by the control plane at https://cp.example") {
		t.Fatalf("published limits not used: %+v %s", s, s.note())
	}
	w := WorkloadEntity{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHosted}}
	w.Spec.Kind = v1alpha1.KindService
	w.Spec.Resources = v1alpha1.Resources{CPURequestMillicores: 250, MemoryRequestBytes: 256 << 20}
	e := &KCLEntities{Workloads: []WorkloadEntity{w}}
	if got := claimableIneligibility(e, s.limits); len(got) != 0 {
		t.Errorf("a shape at the published caps is refused: %v", got)
	}
	if got := claimableIneligibility(e, deploy.ClaimableLimitsFallback()); len(got) == 0 {
		t.Error("the compiled-in copy admitted a shape over its caps")
	}
	for name, c := range map[string]struct {
		av  cloud.ClaimableAvailability
		ok  bool
		err error
		why string
	}{
		"offline":      {err: errors.New("dial tcp: no route"), why: "could not ask https://cp.example"},
		"no rpc":       {ok: false, why: "does not answer GetClaimableAvailability"},
		"no limits":    {ok: true, why: "does not publish its claimable limits"},
		"empty limits": {ok: true, av: cloud.ClaimableAvailability{Limits: &cloud.ClaimableLimits{}}, why: "incomplete"},
	} {
		s := claimableLimitsFrom(ep, c.av, c.ok, c.err)
		if s.published || s.limits != deploy.ClaimableLimitsFallback() {
			t.Errorf("%s: want the compiled-in copy, got %+v", name, s)
		}
		if n := s.note(); !strings.Contains(n, "compiled-in copy (offline: ") || !strings.Contains(n, c.why) {
			t.Errorf("%s: note %q does not say it is offline and why (%q)", name, n, c.why)
		}
	}
}

func TestRefuseClaimableFlags(t *testing.T) {
	t.Parallel()
	var f deployCmdFlags
	if err := refuseClaimableFlags("prod", f); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*deployCmdFlags){
		"--dry-run":                  func(f *deployCmdFlags) { f.dryRun = true },
		"--explain":                  func(f *deployCmdFlags) { f.explain = true },
		"--target":                   func(f *deployCmdFlags) { f.targets = []string{"api"} },
		"a release":                  func(f *deployCmdFlags) { f.promote.version = "v1" },
		"--plan":                     func(f *deployCmdFlags) { f.promote.plan = true },
		"--dry-run with --plan-only": func(f *deployCmdFlags) { f.dryRun, f.promote.planOnly = true, true },
		"--offline-":                 func(f *deployCmdFlags) { f.offlineLedger = true },
	} {
		g := deployCmdFlags{}
		mod(&g)
		if err := refuseClaimableFlags("prod", g); err == nil || !strings.Contains(err.Error(), "--claimable cannot be combined") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// --plan-only is the local eligibility preview, with or without --plan.
	for _, g := range []deployCmdFlags{{promote: promoteCmdFlags{planOnly: true}}, {promote: promoteCmdFlags{plan: true, planOnly: true}}} {
		if err := refuseClaimableFlags("prod", g); err != nil {
			t.Errorf("--plan-only must be allowed: %v", err)
		}
	}
}

// An unavailable platform is named cause by cause, and an anonymous deploy is
// never told to sign up as its remedy: that is offered only as the
// alternative.
func TestClaimableUnavailableError_NamesEachCause(t *testing.T) {
	t.Parallel()
	ep := cloud.Endpoint{URL: "https://admin.example.test"}
	err := claimableUnavailableError("prod", ep, []cloud.ClaimableCause{
		{Code: cloud.ClaimableCauseDisabled, Component: "claimable_deploys.enabled", Message: "an operator has switched off anonymous (claimable) deploys on this platform (its configured default)"},
		{Code: cloud.ClaimableCauseDependencyUnavailable, Component: "content_scan_web_risk", Message: "the Web Risk reputation scan every anonymous upload must pass is required but not configured"},
		{Code: cloud.ClaimableCauseNotSupported, Component: "network_policing", Message: "anonymous services and databases cannot run on this platform yet"},
	})
	for _, want := range []string{
		`Claimable (anonymous) deploys are not available at https://admin.example.test for env "prod". Nothing was created.`,
		"disabled by operator:",
		"unavailable dependency (content_scan_web_risk):",
		"not supported yet:",
		"alternative: deploy to your own account instead: forge login",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%s", want, err)
		}
	}
	if exitCodeForError(err) != exitRefused {
		t.Errorf("exit = %d, want 4", exitCodeForError(err))
	}
	facts := errorFactsOf(err)
	if facts.ErrorReason != cloud.ReasonClaimableUnavailable || facts.UnavailableCause != "disabled" || facts.UnavailableComponent != "claimable_deploys.enabled" {
		t.Errorf("facts = %+v", facts)
	}
}

// The preflight: a redeploy of a stored deploy is not a start, so only the
// compute causes can refuse it; a new deploy is refused by every cause.
func TestAvailabilityCauses(t *testing.T) {
	t.Parallel()
	av := cloud.ClaimableAvailability{
		Causes:        []cloud.ClaimableCause{{Code: "disabled", Component: "claimable_deploys.enabled"}},
		ComputeCauses: []cloud.ClaimableCause{{Code: "not_supported", Component: "network_policing"}},
	}
	if got := availabilityCauses(av, true, false); len(got) != 1 || got[0].Component != "claimable_deploys.enabled" {
		t.Errorf("new static site: %+v", got)
	}
	if got := availabilityCauses(av, true, true); len(got) != 2 {
		t.Errorf("new service: %+v", got)
	}
	if got := availabilityCauses(av, false, false); len(got) != 0 {
		t.Errorf("redeploy of a static site: %+v", got)
	}
	if got := availabilityCauses(av, false, true); len(got) != 1 || got[0].Component != "network_policing" {
		t.Errorf("redeploy of a service: %+v", got)
	}
}

// An unavailable platform refuses BEFORE any proof of work, start or build.
func TestClaimableDeploy_UnavailableIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	fake.availability = &cloud.ClaimableAvailability{Causes: []cloud.ClaimableCause{
		{Code: "disabled", Component: "claimable_deploys.enabled", Message: "an operator has switched off anonymous (claimable) deploys on this platform"}}}

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--json")
	if err == nil || exitCodeForError(err) != exitRefused {
		t.Fatalf("want exit 4; err=%v\n%s", err, out)
	}
	if !strings.Contains(err.Error(), "disabled by operator:") {
		t.Errorf("refusal text:\n%s", err)
	}
	if fake.challenges != 0 || fake.starts != 0 {
		t.Errorf("an unavailable platform still got challenges=%d starts=%d", fake.challenges, fake.starts)
	}
	if doc := decodeClaimDoc(t, out); doc["error_reason"] != "claimable_unavailable" || doc["unavailable_cause"] != "disabled" {
		t.Errorf("json facts = %v", doc)
	}
}

// --claimable --plan-only is a local preview: eligibility plus the platform's
// answer, with no identity, no challenge and no start.
func TestClaimableDeploy_PlanOnlyPreviewsAndCreatesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake, _ := claimableFixture(t)
	fake.availability = &cloud.ClaimableAvailability{Causes: []cloud.ClaimableCause{
		{Code: "dependency_unavailable", Component: "claim_secret", Message: "the platform's claim-link signing secret is not configured"}}}

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--plan-only", "--json")
	if err != nil {
		t.Fatalf("an eligible env must preview with exit 0: %v\n%s", err, out)
	}
	doc := decodeClaimDoc(t, out)
	if doc["ok"] != true || doc["eligible"] != true {
		t.Fatalf("doc = %v", doc)
	}
	if bc, _ := doc["blocking_causes"].([]any); len(bc) != 1 {
		t.Errorf("blocking_causes = %v, want the platform's one cause reported", doc["blocking_causes"])
	}
	if fake.challenges != 0 || fake.starts != 0 || len(fake.authSeen) != 0 {
		t.Errorf("a preview touched the account path: challenges=%d starts=%d authed=%d", fake.challenges, fake.starts, len(fake.authSeen))
	}
}

// The error a claimed deploy yields is a refusal, not a queued deploy.
func TestClaimableDeployError_QueuedIsRefusedNotHeld(t *testing.T) {
	t.Parallel()
	err := claimableDeployError("prod", &deployQueuedError{Env: "prod", Holds: []deploytarget.HostedHold{{Kind: "billing"}}})
	var r *claimableRefusal
	if !errors.As(err, &r) || r.exit != exitRefused {
		t.Fatalf("err = %v", err)
	}
}

// Every user-facing statement of the anonymous plan's caps is derived from
// the compiled-in fallback, so a change to its numbers cannot leave a string
// behind: the `project new --claimable` help, the note every claimable check
// prints, and the --plan-only preview.
func TestClaimableUserFacingTextDerivesFromTheFallbackLimits(t *testing.T) {
	t.Parallel()
	fb := deploy.ClaimableLimitsFallback()
	shape := fb.WorkloadShape()
	storage := fb.DatabaseStorage()
	if want := "125m / 128 MiB"; shape != want {
		t.Fatalf("WorkloadShape() = %q, want %q", shape, want)
	}
	if want := "256 MiB of data (1 GiB volume)"; storage != want {
		t.Fatalf("DatabaseStorage() = %q, want %q", storage, want)
	}

	flag := newNewCmd().Flags().Lookup("claimable")
	if flag == nil || !strings.Contains(flag.Usage, "("+shape+", request and limit)") {
		t.Errorf("`project new --claimable` help does not state the fallback shape %q: %v", shape, flag)
	}

	n := claimableLimitsSource{limits: fb, why: "test"}.note()
	for _, want := range []string{shape + " per workload", "Postgres storage " + storage} {
		if !strings.Contains(n, want) {
			t.Errorf("note %q lacks %q", n, want)
		}
	}
	if err := claimableIneligibleError("prod", fb, []string{"x"}); !strings.Contains(err.Error(), "at most "+shape+", request and limit") {
		t.Errorf("ineligible error does not state %q: %v", shape, err)
	}
}

// The human --plan-only preview prints the caps it judged by, from the
// fallback when the control plane publishes none.
func TestClaimableDeploy_PlanOnlyPreviewStatesTheFallbackLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	claimableFixture(t)
	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--plan-only")
	if err != nil {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	fb := deploy.ClaimableLimitsFallback()
	for _, want := range []string{
		"Postgres storage " + fb.DatabaseStorage(),
		fb.WorkloadShape() + " per workload",
		"one service (at most " + fb.WorkloadShape() + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
}
