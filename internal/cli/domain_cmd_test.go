package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// A fake controlplane.v1.DomainService, routed by procedure path exactly as
// the real one is. Every test drives the REAL cloud.Client against it over
// HTTP, so the Connect JSON binding, the Authorization header and the
// request bodies are all proven rather than assumed.
type fakeDomainCP struct {
	t *testing.T
	// bodies records each procedure's decoded request, so a test can assert
	// what forge SENT and not only what it printed.
	bodies map[string]map[string]any
	// replies is the JSON each procedure answers with, by short name.
	replies map[string]string
	// order is every procedure called, in order.
	order []string
}

func newFakeDomainCP(t *testing.T, replies map[string]string) *fakeDomainCP {
	return &fakeDomainCP{t: t, bodies: map[string]map[string]any{}, replies: replies}
}

func (f *fakeDomainCP) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		short := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.bodies[short] = body
		f.order = append(f.order, short)
		reply, ok := f.replies[short]
		if !ok {
			f.t.Errorf("unexpected procedure %s", r.URL.Path)
			reply = "{}"
		}
		_, _ = w.Write([]byte(reply))
	})
}

// domainTestClient wires the real client to the fake over HTTP.
func domainTestClient(t *testing.T, f *fakeDomainCP) cloudCaller {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	ep, err := cloud.ResolveEndpoint("prod", &cloud.Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return cloud.NewClient(ep, cloud.Credential{Token: "tok"})
}

const domainPendingJSON = `{"domain":{"id":"dom_01","hostname":"hounders.club",
  "state":"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS","source":"DOMAIN_SOURCE_EXTERNAL",
  "requiredRecords":[
    {"type":"A","name":"hounders.club","value":"34.63.203.181"},
    {"type":"TXT","name":"_reliant-challenge.hounders.club","value":"tok-123"}]}}`

// `forge domain add` prints the records to publish as a copyable table.
// THAT TABLE IS THE POINT: until the author pastes it into their registrar
// nothing converges, and forge cannot do it for them.
//
// MUTATION VERIFIED RED: dropping the writeDNSTable call from runDomainAdd
// leaves the command reporting success while printing nothing actionable.
func TestRunDomainAdd_PrintsTheDNSToPublish(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"CreateDomain": domainPendingJSON})
	var out bytes.Buffer
	if err := runDomainAdd(context.Background(), domainTestClient(t, f), "hounders.club", false, &out); err != nil {
		t.Fatalf("runDomainAdd: %v", err)
	}
	if got := f.bodies["CreateDomain"]["hostname"]; got != "hounders.club" {
		t.Errorf("CreateDomain sent hostname %v", got)
	}
	rendered := out.String()
	for _, want := range []string{
		"Added hounders.club (pending_dns)",
		"TYPE", "NAME", "VALUE",
		"A", "hounders.club", "34.63.203.181",
		"TXT", "_reliant-challenge.hounders.club", "tok-123",
		"forge domain verify hounders.club",
		"serves nothing until you bind it",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("add output is missing %q:\n%s", want, rendered)
		}
	}
}

// --json on a read emits the decoded resource, so a script never parses the
// table.
func TestRunDomainAdd_JSON(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"CreateDomain": domainPendingJSON})
	var out bytes.Buffer
	if err := runDomainAdd(context.Background(), domainTestClient(t, f), "hounders.club", true, &out); err != nil {
		t.Fatalf("runDomainAdd: %v", err)
	}
	var got struct {
		Domain struct {
			Hostname        string `json:"hostname"`
			RequiredRecords []struct {
				Type, Name, Value string
			} `json:"requiredRecords"`
		} `json:"domain"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", err, out.String())
	}
	if got.Domain.Hostname != "hounders.club" || len(got.Domain.RequiredRecords) != 2 {
		t.Errorf("decoded JSON = %+v", got.Domain)
	}
	if strings.Contains(out.String(), "Set these DNS records") {
		t.Error("--json emitted the human table as well")
	}
}

// ls names every domain and what it serves. A domain has no environment, so
// the binding is where the env appears at all.
func TestRunDomainList_NamesWhatEachDomainServes(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"ListDomains": `{"domains":[
      {"id":"d1","hostname":"hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE",
       "binding":{"id":"b1","domainId":"d1","environmentId":"env-1","target":"web"}},
      {"id":"d2","hostname":"www.hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE",
       "binding":{"id":"b2","domainId":"d2","environmentId":"env-1","redirectTo":"hounders.club"}},
      {"id":"d3","hostname":"shop.hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS"}]}`})
	var out bytes.Buffer
	if err := runDomainList(context.Background(), domainTestClient(t, f), false, &out); err != nil {
		t.Fatalf("runDomainList: %v", err)
	}
	rendered := out.String()
	for _, want := range []string{
		"HOSTNAME", "STATE", "SERVING",
		"hounders.club", "live", "web in env-1",
		"www.hounders.club", "→ hounders.club (redirect)",
		"shop.hounders.club", "pending_dns", "(unbound)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("ls output is missing %q:\n%s", want, rendered)
		}
	}
}

// An org with no domains is a normal state, not a failure — and the empty
// line says how to get one.
func TestRunDomainList_EmptyIsNotAnError(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"ListDomains": `{"domains":[]}`})
	var out bytes.Buffer
	if err := runDomainList(context.Background(), domainTestClient(t, f), false, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "forge domain add") {
		t.Errorf("the empty state should name the command that fixes it; got %q", out.String())
	}
}

// A state name this forge has never heard of decodes as "unknown", never
// toward live. Pins that the CLI shares deploytarget.DomainStateName rather
// than carrying a second decoder that could drift.
func TestRunDomainShow_UnknownStateIsNeverLive(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{
		"GetDomain": `{"domain":{"id":"d1","hostname":"hounders.club",
          "state":"DEPLOY_CUSTOM_DOMAIN_STATE_SOMETHING_NEW","source":"DOMAIN_SOURCE_WHAT",
          "lastError":"CAA record forbids this issuer"}}`,
	})
	var out bytes.Buffer
	if err := runDomainShow(context.Background(), domainTestClient(t, f), "hounders.club", false, &out); err != nil {
		t.Fatalf("runDomainShow: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "state:   unknown") {
		t.Errorf("an unrecognised state must read as unknown:\n%s", rendered)
	}
	if !strings.Contains(rendered, "source:  unknown") {
		t.Errorf("an unrecognised source must read as unknown:\n%s", rendered)
	}
	if strings.Contains(rendered, "live") {
		t.Errorf("an unrecognised state must not read as live:\n%s", rendered)
	}
	if !strings.Contains(rendered, "CAA record forbids this issuer") {
		t.Errorf("the last error is the actionable part:\n%s", rendered)
	}
}

// verify resolves the hostname to an id and then nudges by ID — the RPC
// takes domain_id, and a human has the name.
func TestRunDomainVerify_ResolvesHostnameThenVerifiesByID(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{
		"GetDomain": domainPendingJSON,
		"VerifyDomain": `{"domain":{"id":"dom_01","hostname":"hounders.club",
          "state":"DEPLOY_CUSTOM_DOMAIN_STATE_VERIFYING"}}`,
	})
	var out bytes.Buffer
	if err := runDomainVerify(context.Background(), domainTestClient(t, f), "hounders.club", false, &out); err != nil {
		t.Fatalf("runDomainVerify: %v", err)
	}
	if got := f.bodies["GetDomain"]["hostname"]; got != "hounders.club" {
		t.Errorf("GetDomain sent %v, want the hostname", got)
	}
	if got := f.bodies["VerifyDomain"]["domainId"]; got != "dom_01" {
		t.Errorf("VerifyDomain sent domainId %v, want the resolved id", got)
	}
	if !strings.Contains(out.String(), "verifying") {
		t.Errorf("verify should print the resulting state:\n%s", out.String())
	}
}

// bind resolves --env through the hosted READ path and sends that id.
//
// THE SAFETY PROPERTY: it must not create an environment. A typo in --env
// would otherwise mint an empty env and bind a public hostname to it, and
// the command would report success.
//
// MUTATION VERIFIED RED: switching domainEnvironmentID to EnsureEnvironment
// puts EnsureEnvironment in the call order.
func TestRunDomainBind_ResolvesEnvByReadAndNeverCreatesIt(t *testing.T) {
	prev := hostedProjectName
	hostedProjectName = func() string { return "hounders" }
	t.Cleanup(func() { hostedProjectName = prev })

	f := newFakeDomainCP(t, map[string]string{
		"GetDomain":           domainPendingJSON,
		"ListEnvironments":    `{"environments":[{"id":"env-7","name":"prod","project":"hounders"}]}`,
		"CreateDomainBinding": `{"binding":{"id":"b1","domainId":"dom_01","environmentId":"env-7","target":"web"}}`,
	})
	var out bytes.Buffer
	c := domainTestClient(t, f)
	if err := runDomainBind(context.Background(), c, "prod", "hounders.club", "web", "", &out); err != nil {
		t.Fatalf("runDomainBind: %v", err)
	}
	for _, proc := range f.order {
		if proc == "EnsureEnvironment" {
			t.Fatalf("bind created an environment instead of reading it: %v", f.order)
		}
	}
	body := f.bodies["CreateDomainBinding"]
	if body["domainId"] != "dom_01" || body["environmentId"] != "env-7" || body["target"] != "web" {
		t.Errorf("CreateDomainBinding sent %+v", body)
	}
	if _, ok := body["redirectTo"]; ok {
		t.Error("a --target bind must not also send redirectTo")
	}
	rendered := out.String()
	// A bound domain that is not live yet is waiting on the author's DNS,
	// so bind repeats the records rather than going quiet.
	for _, want := range []string{"Bound hounders.club → web, in prod", "pending_dns", "34.63.203.181"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("bind output is missing %q:\n%s", want, rendered)
		}
	}
}

// A redirect bind sends redirect_to and no target: the apex/www pair.
func TestRunDomainBind_RedirectSendsRedirectToOnly(t *testing.T) {
	prev := hostedProjectName
	hostedProjectName = func() string { return "hounders" }
	t.Cleanup(func() { hostedProjectName = prev })

	f := newFakeDomainCP(t, map[string]string{
		"GetDomain": `{"domain":{"id":"dom_02","hostname":"www.hounders.club",
          "state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE"}}`,
		"ListEnvironments":    `{"environments":[{"id":"env-7","name":"prod","project":"hounders"}]}`,
		"CreateDomainBinding": `{"binding":{"id":"b2","domainId":"dom_02","environmentId":"env-7","redirectTo":"hounders.club"}}`,
	})
	var out bytes.Buffer
	if err := runDomainBind(context.Background(), domainTestClient(t, f), "prod", "www.hounders.club", "", "hounders.club", &out); err != nil {
		t.Fatalf("runDomainBind: %v", err)
	}
	body := f.bodies["CreateDomainBinding"]
	if body["redirectTo"] != "hounders.club" {
		t.Errorf("CreateDomainBinding sent %+v, want redirectTo", body)
	}
	if _, ok := body["target"]; ok {
		t.Error("a --redirect-to bind must not also send target")
	}
	if !strings.Contains(out.String(), "308 redirect") {
		t.Errorf("a redirect bind should say so:\n%s", out.String())
	}
	// Already live: no DNS table, because there is nothing left to do.
	if strings.Contains(out.String(), "Set these DNS records") {
		t.Errorf("a live domain has no outstanding DNS step:\n%s", out.String())
	}
}

// target and redirect-to are mutually exclusive, and so is neither. Checked
// at the flag layer, before any call, so a malformed bind costs no round
// trip and the message names both spellings.
func TestDomainBind_RefusesTargetAndRedirectTogether(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"both", []string{"domain", "bind", "hounders.club", "--env", "prod", "--target", "web", "--redirect-to", "x.club"}},
		{"neither", []string{"domain", "bind", "hounders.club", "--env", "prod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestRootCmd(t)
			root.SetArgs(tc.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), "exactly one of --target or --redirect-to") {
				t.Errorf("refusal should name both flags: %v", err)
			}
		})
	}
}

// unbind keeps the domain, and says so — the difference from rm is the
// whole reason both exist.
func TestRunDomainUnbind_KeepsTheDomain(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{
		"GetDomain": `{"domain":{"id":"dom_01","hostname":"hounders.club",
          "state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE",
          "binding":{"id":"b1","domainId":"dom_01","environmentId":"env-7","target":"web"}}}`,
		"DeleteDomainBinding": `{}`,
	})
	var out bytes.Buffer
	if err := runDomainUnbind(context.Background(), domainTestClient(t, f), "hounders.club", &out); err != nil {
		t.Fatalf("runDomainUnbind: %v", err)
	}
	if got := f.bodies["DeleteDomainBinding"]["domainId"]; got != "dom_01" {
		t.Errorf("DeleteDomainBinding sent %v, want the domain id", got)
	}
	if !strings.Contains(out.String(), "verification are kept") {
		t.Errorf("unbind must say the domain survives:\n%s", out.String())
	}
}

// Unbinding something that serves nothing is a no-op, not an error, and
// costs no write.
func TestRunDomainUnbind_AlreadyUnboundIsNotAnError(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"GetDomain": domainPendingJSON})
	var out bytes.Buffer
	if err := runDomainUnbind(context.Background(), domainTestClient(t, f), "hounders.club", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, proc := range f.order {
		if proc == "DeleteDomainBinding" {
			t.Error("an already-unbound domain should cost no write")
		}
	}
	if !strings.Contains(out.String(), "serves nothing already") {
		t.Errorf("got %q", out.String())
	}
}

// rm gives the hostname up, and says the thing that matters: another org
// can now claim it. That is the only self-service escape from a conflict.
func TestRunDomainRemove_SaysTheHostnameIsFreed(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{
		"GetDomain": domainPendingJSON, "DeleteDomain": `{}`,
	})
	var out bytes.Buffer
	if err := runDomainRemove(context.Background(), domainTestClient(t, f), "hounders.club", &out); err != nil {
		t.Fatalf("runDomainRemove: %v", err)
	}
	if got := f.bodies["DeleteDomain"]["domainId"]; got != "dom_01" {
		t.Errorf("DeleteDomain sent %v", got)
	}
	if !strings.Contains(out.String(), "free for another organization to claim") {
		t.Errorf("rm should say the hostname is released:\n%s", out.String())
	}
}

// Every domain command authenticates the way `forge secret` and
// `forge cloud` do, and addresses the real DomainService procedure paths.
func TestDomainCommands_AuthenticateAndUseTheDomainServicePaths(t *testing.T) {
	var gotAuth string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(domainPendingJSON))
	}))
	defer srv.Close()

	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("ACME_DEPLOY_TOKEN", "ci-token")
	entities := &KCLEntities{ControlPlane: &ControlPlaneEntity{
		Type: "control_plane", Endpoint: srv.URL, TokenEnv: "ACME_DEPLOY_TOKEN",
	}}
	ep, err := cloud.ResolveEndpoint("prod", declarationFromEntities(entities))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runDomainAdd(context.Background(), cloud.NewClient(ep, cred), "hounders.club", false, &out); err != nil {
		t.Fatalf("runDomainAdd: %v", err)
	}
	if gotAuth != "Bearer ci-token" {
		t.Errorf("Authorization: got %q — the declared token env var must win over the credentials file", gotAuth)
	}
	if len(paths) != 1 || paths[0] != "/controlplane.v1.DomainService/CreateDomain" {
		t.Errorf("procedure path: got %v", paths)
	}
}

// --env is what selects the control plane, so a domain command without one
// must say that rather than fail somewhere downstream about credentials.
func TestDomainClient_RequiresEnv(t *testing.T) {
	_, err := domainClient(context.Background(), "  ", "")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "--env is required") ||
		!strings.Contains(err.Error(), "which control plane") {
		t.Errorf("the message should explain what --env selects: %v", err)
	}
}

// ── Per-record verdicts ──────────────────────────────────────────────────────

// A domain whose apex is right and whose TXT is wrong — the case the
// per-record checks exist for, and the one a single domain-level error
// cannot express.
const domainMixedVerdictJSON = `{"domain":{"id":"dom_01","hostname":"hounders.club",
  "state":"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS","source":"DOMAIN_SOURCE_EXTERNAL",
  "requiredRecords":[
    {"type":"A","name":"hounders.club","value":"34.63.203.181","resolved":true},
    {"type":"TXT","name":"_reliant-challenge.hounders.club","value":"tok-123",
     "detail":"no TXT record found at _reliant-challenge.hounders.club"}]}}`

// THE REGRESSION TEST. forge re-encodes the control plane's response into
// its own local struct, so a field it does not declare is a field it drops —
// silently, and from `--json` as well as the table. The verdicts were being
// dropped exactly that way, which left `forge domain show` on a stuck domain
// saying only "not verified yet" while the control plane knew precisely
// which record was wrong and why.
//
// MUTATION VERIFIED RED: removing Resolved/Detail from domainWireRecord
// leaves this failing on every assertion below.
func TestRunDomainShow_CarriesThePerRecordVerdicts(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"GetDomain": domainMixedVerdictJSON})
	var out bytes.Buffer
	if err := runDomainShow(context.Background(), domainTestClient(t, f), "hounders.club", false, &out); err != nil {
		t.Fatalf("runDomainShow: %v", err)
	}
	rendered := out.String()
	// The passing record says so, and the failing one says WHY — the
	// detail is what names the fix.
	for _, want := range []string{
		"STATUS",
		"ok",
		"NOT YET — no TXT record found at _reliant-challenge.hounders.club",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("show output is missing %q:\n%s", want, rendered)
		}
	}
}

// --json carries them too, so a script sees what the table shows. The two
// come from one struct, which is what keeps them in agreement.
func TestRunDomainShow_JSONCarriesThePerRecordVerdicts(t *testing.T) {
	f := newFakeDomainCP(t, map[string]string{"GetDomain": domainMixedVerdictJSON})
	var out bytes.Buffer
	if err := runDomainShow(context.Background(), domainTestClient(t, f), "hounders.club", true, &out); err != nil {
		t.Fatalf("runDomainShow: %v", err)
	}
	var got struct {
		Domain struct {
			RequiredRecords []struct {
				Type     string `json:"type"`
				Resolved bool   `json:"resolved"`
				Detail   string `json:"detail"`
			} `json:"requiredRecords"`
		} `json:"domain"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parsing --json output: %v\n%s", err, out.String())
	}
	if len(got.Domain.RequiredRecords) != 2 {
		t.Fatalf("got %d records, want 2", len(got.Domain.RequiredRecords))
	}
	for _, rec := range got.Domain.RequiredRecords {
		switch rec.Type {
		case "A":
			if !rec.Resolved {
				t.Error("the A record's resolved=true was dropped on the way through forge")
			}
		case "TXT":
			if rec.Resolved {
				t.Error("the TXT record reports resolved, but the control plane said it was not")
			}
			if rec.Detail == "" {
				t.Error("the TXT record's detail was dropped; it is what names the fix")
			}
		}
	}
}

// "Not checked yet" is a third state, and must not read as a failure. On a
// brand-new domain it describes EVERY record, so rendering it as one sends
// the author to re-check something that is perfectly correct.
func TestWriteDNSTable_UncheckedRecordIsNotRenderedAsAFailure(t *testing.T) {
	var out bytes.Buffer
	writeDNSTable(&out, []domainWireRecord{
		{Type: "A", Name: "hounders.club", Value: "34.63.203.181"},
	})
	rendered := out.String()
	if !strings.Contains(rendered, "not checked yet") {
		t.Errorf("an unchecked record should say so:\n%s", rendered)
	}
	if strings.Contains(rendered, "NOT YET") {
		t.Errorf("an unchecked record was rendered as a failure:\n%s", rendered)
	}
}
