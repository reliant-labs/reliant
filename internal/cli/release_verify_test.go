package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// Verification tests run entirely against a stubbed transport.
//
// Not hitting the real network is a correctness requirement, not a speed
// preference. The interesting cases here are a registry serving a DIFFERENT
// hash than the ledger records, and a registry that times out — neither can be
// produced on demand against npmjs.org. A test that reached the real registry
// would also invert its own meaning: it would pass whenever the network agreed
// with the code, including when the code was wrong.

// stubFetcher is a scripted httpFetcher. Responses are keyed by a substring of
// the URL so a test states the coordinate it cares about without reproducing
// the exact escaping the code builds.
type stubFetcher struct {
	mu sync.Mutex
	// responses maps a URL substring → the reply to serve.
	responses []stubResponse
	// calls records every URL requested, in order, so a test can assert
	// which endpoint was hit (e.g. that the ZIP hash line was read from the
	// checksum DB rather than the proxy's cacheable .info).
	calls []string
}

type stubResponse struct {
	match  string
	status int
	body   string
	header http.Header
	err    error
}

func (s *stubFetcher) Fetch(_ context.Context, req fetchRequest) (fetchResponse, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req.URL)
	s.mu.Unlock()
	for _, r := range s.responses {
		if strings.Contains(req.URL, r.match) {
			if r.err != nil {
				return fetchResponse{}, r.err
			}
			h := r.header
			if h == nil {
				h = http.Header{}
			}
			return fetchResponse{StatusCode: r.status, Body: []byte(r.body), Header: h}, nil
		}
	}
	return fetchResponse{StatusCode: http.StatusNotFound, Body: []byte("{}"), Header: http.Header{}}, nil
}

func (s *stubFetcher) requested(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// npmPackumentJSON builds a registry packument carrying one version→integrity
// pair per entry.
func npmPackumentJSON(versionToIntegrity map[string]string) string {
	parts := make([]string, 0, len(versionToIntegrity))
	for v, integrity := range versionToIntegrity {
		parts = append(parts, fmt.Sprintf(`%q:{"dist":{"integrity":%q}}`, v, integrity))
	}
	return `{"versions":{` + strings.Join(parts, ",") + `}}`
}

// ── npm ─────────────────────────────────────────────────────────────────────

// TestVerifyNPM_Verified is the happy path: the registry has the version and
// serves back exactly the integrity hash the ledger recorded.
func TestVerifyNPM_Verified(t *testing.T) {
	const integrity = "sha512-2MRv6GYvK71IPKJl0xjuqPNw7rxlQT48zfFIEmO1GWcPcpDbe/5gdmi6oMMj5Zn8Wi0BPuCBF21zqI1hVSjc+g=="
	f := &stubFetcher{responses: []stubResponse{{
		match:  "forge-web-runtime",
		status: http.StatusOK,
		body:   npmPackumentJSON(map[string]string{"0.3.0": "sha512-old", "0.3.1": integrity}),
	}}}

	got := verifyNPMArtifact(context.Background(), f, "@reliantlabs/forge-web-runtime", release.Artifact{
		Kind: release.KindNPM, Version: "0.3.1", Integrity: integrity,
	})

	if got.Status != verifyVerified {
		t.Fatalf("status = %v (%s), want VERIFIED", got.Status, got.Detail)
	}
}

// TestVerifyNPM_VersionNeverPublished IS THE v0.1.12 BUG, REPRODUCED.
//
// That release tagged web-runtime v0.3.1 and recorded the integrity hash of
// the tarball `npm pack` produced on the build machine — bytes that were real,
// but that never reached the registry because the publish step never ran. The
// registry therefore had 0.3.0 and nothing else.
//
// The ledger's claim was internally consistent and completely false, and
// nothing in forge compared it to the world. This test is that comparison: the
// artifact must come back FAILED, and the message must name which versions the
// registry actually has, because "0.3.1 is missing but 0.3.0 is there" is what
// tells a human the publish step is what broke.
func TestVerifyNPM_VersionNeverPublished(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match:  "forge-web-runtime",
		status: http.StatusOK,
		// The registry as it really was: 0.3.0 published, 0.3.1 never.
		body: npmPackumentJSON(map[string]string{"0.3.0": "sha512-theOnlyOneThatShipped"}),
	}}}

	got := verifyNPMArtifact(context.Background(), f, "@reliantlabs/forge-web-runtime", release.Artifact{
		Kind:      release.KindNPM,
		Version:   "0.3.1",
		Integrity: "sha512-hashOfBytesThatOnlyExistedOnTheBuildMachine",
	})

	if got.Status != verifyFailed {
		t.Fatalf("a release naming an unpublished npm version MUST fail verification; got %v (%s)", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "0.3.1") || !strings.Contains(got.Detail, "NOT published") {
		t.Errorf("detail must name the missing version: %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "0.3.0") {
		t.Errorf("detail must name what the registry DOES have, so a human can see the publish step is the gap: %q", got.Detail)
	}
}

// TestVerifyNPM_IntegrityMismatch covers the other half of the npm contract:
// the version exists, but different bytes shipped under it. npm forbids
// republishing a version, so this is not self-correcting and must be loud.
func TestVerifyNPM_IntegrityMismatch(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match:  "widget",
		status: http.StatusOK,
		body:   npmPackumentJSON(map[string]string{"1.0.0": "sha512-whatTheRegistryActuallyServes"}),
	}}}

	got := verifyNPMArtifact(context.Background(), f, "widget", release.Artifact{
		Kind: release.KindNPM, Version: "1.0.0", Integrity: "sha512-whatTheLedgerRecorded",
	})

	if got.Status != verifyFailed {
		t.Fatalf("status = %v (%s), want FAILED", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "MISMATCH") {
		t.Errorf("detail should say what specifically differed: %q", got.Detail)
	}
	// Both hashes must appear: a mismatch report that shows only one side
	// leaves the reader unable to tell which artifact is the odd one out.
	if !strings.Contains(got.Detail, "whatTheLedgerRecorded") || !strings.Contains(got.Detail, "whatTheRegistryActuallyServes") {
		t.Errorf("detail must show BOTH hashes: %q", got.Detail)
	}
}

// TestVerifyNPM_PackageAbsent distinguishes "the package does not exist at
// all" from "this version of it does not".
func TestVerifyNPM_PackageAbsent(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "ghost", status: http.StatusNotFound, body: `{"error":"Not found"}`,
	}}}

	got := verifyNPMArtifact(context.Background(), f, "ghost", release.Artifact{
		Kind: release.KindNPM, Version: "1.0.0", Integrity: "sha512-x",
	})
	if got.Status != verifyFailed {
		t.Fatalf("status = %v (%s), want FAILED", got.Status, got.Detail)
	}
}

// TestVerifyNPM_NetworkErrorIsUnreachable is the anti-flake guard. A transport
// failure means the check did not run — it is NOT evidence the artifact is
// missing. Collapsing the two is what turns a verifier into a CI gate that
// fails on a bad DNS day and gets switched off the following week.
func TestVerifyNPM_NetworkErrorIsUnreachable(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "widget", err: errors.New("dial tcp: i/o timeout"),
	}}}

	got := verifyNPMArtifact(context.Background(), f, "widget", release.Artifact{
		Kind: release.KindNPM, Version: "1.0.0", Integrity: "sha512-x",
	})
	if got.Status != verifyUnreachable {
		t.Fatalf("a timeout must be UNREACHABLE, not a verdict on the artifact; got %v (%s)", got.Status, got.Detail)
	}
}

// TestVerifyNPM_ServerErrorIsUnreachable: a 503 is the registry having a bad
// day, which says nothing about whether the artifact was published.
func TestVerifyNPM_ServerErrorIsUnreachable(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "widget", status: http.StatusServiceUnavailable, body: "upstream down",
	}}}

	got := verifyNPMArtifact(context.Background(), f, "widget", release.Artifact{
		Kind: release.KindNPM, Version: "1.0.0", Integrity: "sha512-x",
	})
	if got.Status != verifyUnreachable {
		t.Fatalf("status = %v (%s), want UNREACHABLE", got.Status, got.Detail)
	}
}

// TestVerifyNPM_NoIntegrityIsUnverifiable: the version exists, but with no
// recorded hash nothing was COMPARED. Reporting that as verified would claim a
// byte check that never happened.
func TestVerifyNPM_NoIntegrityIsUnverifiable(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "widget", status: http.StatusOK,
		body: npmPackumentJSON(map[string]string{"1.0.0": "sha512-real"}),
	}}}

	got := verifyNPMArtifact(context.Background(), f, "widget", release.Artifact{
		Kind: release.KindNPM, Version: "1.0.0", // no Integrity
	})
	if got.Status != verifyUnverifiable {
		t.Fatalf("status = %v (%s), want UNVERIFIABLE", got.Status, got.Detail)
	}
}

// TestVerifyNPM_ScopedNameIsEscaped pins the URL shape for a scoped package:
// the '/' in @scope/name must be percent-encoded, which is how npm's own
// clients address it.
func TestVerifyNPM_ScopedNameIsEscaped(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "registry.npmjs.org", status: http.StatusOK,
		body: npmPackumentJSON(map[string]string{"0.3.1": "sha512-x"}),
	}}}

	verifyNPMArtifact(context.Background(), f, "@reliantlabs/forge-web-runtime", release.Artifact{
		Kind: release.KindNPM, Version: "0.3.1", Integrity: "sha512-x",
	})

	if !f.requested("%2F") {
		t.Errorf("scoped package name should be percent-encoded; requested URLs: %v", f.calls)
	}
}

// ── Go modules ──────────────────────────────────────────────────────────────

// goSumLookupBody reproduces a sum.golang.org /lookup response: a sequence
// number, the module's two go.sum lines, then the signed tree.
func goSumLookupBody(modPath, version, zipHash, modHash string) string {
	return fmt.Sprintf("62791850\n%s %s %s\n%s %s/go.mod %s\n\ngo.sum database tree\n62919564\njI/W1f+i6LC+30NV5tPECwjjs6tvIMKOy7m1bqQgpzE=\n\n— sum.golang.org Az3grlz5\n",
		modPath, version, zipHash, modPath, version, modHash)
}

// publicModuleEnv neutralizes the ambient GOPRIVATE / GONOSUMDB on the
// developer's machine for the duration of a test.
//
// This is not hygiene theatre — it caught a real bug. This repo's own dev boxes
// set GOPRIVATE=github.com/reliant-labs/* and GONOSUMDB=*, so the first run of
// TestVerifyGoModule_Verified reported UNVERIFIABLE for forge's own module and
// the assertion failed. Without this helper the test's meaning would depend on
// whose machine it ran on: green in CI, red locally, or — far worse — a test
// for public-module verification that silently only ever exercised the private
// short-circuit.
func publicModuleEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GONOSUMCHECK", "")
}

// TestVerifyGoModule_Verified is the happy path against a real-shaped
// checksum-DB response.
func TestVerifyGoModule_Verified(t *testing.T) {
	publicModuleEnv(t)
	const mod = "github.com/reliant-labs/forge/pkg"
	const zipHash = "h1:oEi68/DnGrk7r/zJf5IKTC02p6cISJJhsGKjhCULOuw="
	f := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", status: http.StatusOK,
		body: goSumLookupBody(mod, "v0.1.14", zipHash, "h1:sfx6mK8xPMq8RqvfpkNa3mj4mBp7CDOKaNyTZLmX1QE="),
	}}}

	got := verifyGoModuleArtifact(context.Background(), f, mod, release.Artifact{
		Kind: release.KindGoModule, Version: "v0.1.14", Integrity: zipHash,
	})
	if got.Status != verifyVerified {
		t.Fatalf("status = %v (%s), want VERIFIED", got.Status, got.Detail)
	}
}

// TestVerifyGoModule_ComparesZipHashNotGoModHash is the subtle one.
//
// A checksum-DB response carries TWO h1: lines per module: one hashing the
// module ZIP, one hashing its go.mod alone. They hash different bytes and are
// never equal. The ledger records the ZIP hash, so a verifier that grabbed the
// first h1: it found without checking which line it came from would compare a
// content hash against a manifest hash and report a mismatch on a perfectly
// good release — a false alarm that would get the command distrusted.
//
// Here the ledger holds the correct ZIP hash and the go.mod line holds a
// different one. Verification must pass.
func TestVerifyGoModule_ComparesZipHashNotGoModHash(t *testing.T) {
	publicModuleEnv(t)
	const mod = "example.com/thing"
	const zipHash = "h1:ZIPZIPZIPZIPZIPZIPZIPZIPZIPZIPZIPZIPZIPZIPA="
	const goModHash = "h1:MODMODMODMODMODMODMODMODMODMODMODMODMODMODB="

	f := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", status: http.StatusOK,
		body: goSumLookupBody(mod, "v1.2.3", zipHash, goModHash),
	}}}

	got := verifyGoModuleArtifact(context.Background(), f, mod, release.Artifact{
		Kind: release.KindGoModule, Version: "v1.2.3", Integrity: zipHash,
	})
	if got.Status != verifyVerified {
		t.Fatalf("the ZIP hash line must be the one compared; got %v (%s)", got.Status, got.Detail)
	}

	// And the converse: a ledger carrying the go.mod hash where the ZIP hash
	// belongs is a real mismatch and must fail.
	wrong := verifyGoModuleArtifact(context.Background(), f, mod, release.Artifact{
		Kind: release.KindGoModule, Version: "v1.2.3", Integrity: goModHash,
	})
	if wrong.Status != verifyFailed {
		t.Fatalf("a go.mod hash recorded as the module hash must FAIL; got %v (%s)", wrong.Status, wrong.Detail)
	}

	// THE CASE WITH ACTUAL TEETH: the go.mod line FIRST.
	//
	// The two assertions above pass even for a parser that takes the first
	// h1: line it sees, because sum.golang.org happens to emit the ZIP line
	// first — so they confirm the right answer without proving the code
	// earned it. Verified by sabotage: loosening the match to a version
	// PREFIX (which lets "v1.2.3/go.mod" match "v1.2.3") left both green.
	// Reversing the order is what separates an exact "<path> <version>"
	// match from a positional guess, and it is the responsible thing to
	// require regardless: nothing in the response format promises an order.
	reversed := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", status: http.StatusOK,
		body: fmt.Sprintf("62791850\n%s %s/go.mod %s\n%s %s %s\n\ngo.sum database tree\n",
			mod, "v1.2.3", goModHash, mod, "v1.2.3", zipHash),
	}}}
	ordered := verifyGoModuleArtifact(context.Background(), reversed, mod, release.Artifact{
		Kind: release.KindGoModule, Version: "v1.2.3", Integrity: zipHash,
	})
	if ordered.Status != verifyVerified {
		t.Fatalf("the ZIP hash must be selected by an EXACT \"<path> <version>\" match, not by position; got %v (%s)",
			ordered.Status, ordered.Detail)
	}
}

// TestVerifyGoModule_UsesChecksumDBNotCacheableInfo pins the endpoint choice.
//
// proxy.golang.org/<mod>/@v/<version>.info caches NEGATIVE results, so anything
// that polls a version before its tag is pushed leaves a 404 that outlives the
// tag. We hit that repeatedly. The checksum DB is an append-only log with no
// negative caching, so it is the endpoint that answers honestly.
func TestVerifyGoModule_UsesChecksumDBNotCacheableInfo(t *testing.T) {
	publicModuleEnv(t)
	const mod = "example.com/thing"
	f := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", status: http.StatusOK,
		body: goSumLookupBody(mod, "v1.0.0", "h1:zip=", "h1:mod="),
	}}}

	verifyGoModuleArtifact(context.Background(), f, mod, release.Artifact{
		Kind: release.KindGoModule, Version: "v1.0.0", Integrity: "h1:zip=",
	})

	if !f.requested("sum.golang.org") {
		t.Errorf("should query the checksum database; requested: %v", f.calls)
	}
	if f.requested(".info") {
		t.Errorf("must NOT rely on the proxy's .info endpoint — it caches negative results; requested: %v", f.calls)
	}
}

// TestVerifyGoModule_VersionAbsent: a 404 from the checksum DB for a public
// module means no such version was ever fetched from the public proxy.
func TestVerifyGoModule_VersionAbsent(t *testing.T) {
	publicModuleEnv(t)
	f := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", status: http.StatusNotFound, body: "not found",
	}}}

	got := verifyGoModuleArtifact(context.Background(), f, "example.com/thing", release.Artifact{
		Kind: release.KindGoModule, Version: "v9.9.9", Integrity: "h1:x=",
	})
	if got.Status != verifyFailed {
		t.Fatalf("status = %v (%s), want FAILED", got.Status, got.Detail)
	}
}

// TestVerifyGoModule_PrivateIsUnverifiable: the checksum DB has no record of a
// GOPRIVATE module BY DESIGN, so its 404 means "not my department". Failing on
// it would make the command unusable for anyone with a private module.
func TestVerifyGoModule_PrivateIsUnverifiable(t *testing.T) {
	t.Setenv("GOPRIVATE", "corp.internal/*")
	// The fetcher would 404 if consulted — the point is that it is NOT.
	f := &stubFetcher{}

	got := verifyGoModuleArtifact(context.Background(), f, "corp.internal/secret", release.Artifact{
		Kind: release.KindGoModule, Version: "v1.0.0", Integrity: "h1:x=",
	})
	if got.Status != verifyUnverifiable {
		t.Fatalf("a GOPRIVATE module must be UNVERIFIABLE, not FAILED; got %v (%s)", got.Status, got.Detail)
	}
	if len(f.calls) != 0 {
		t.Errorf("a private module should not be sent to the public checksum DB; requested: %v", f.calls)
	}
}

// TestGoModuleIsPrivate_Precedence pins the GONOSUMDB-over-GOPRIVATE rule.
//
// This case is here because the machine that wrote this code exposed it: these
// dev boxes carry GOPRIVATE=github.com/reliant-labs/* AND a blanket
// GONOSUMDB=*, and an implementation that simply checked GOPRIVATE first
// reported forge's own module as private. GONOSUMDB is the specific control
// over checksum-database lookups, so when it is set it decides — GOPRIVATE
// only supplies its default when unset, which is how the go command resolves
// the pair.
func TestGoModuleIsPrivate_Precedence(t *testing.T) {
	t.Run("GONOSUMDB overrides a broader GOPRIVATE", func(t *testing.T) {
		t.Setenv("GOPRIVATE", "github.com/acme/*")
		t.Setenv("GONOSUMDB", "corp.internal/*")
		// GOPRIVATE matches, but the authoritative GONOSUMDB does not.
		if _, private := goModuleIsPrivate("github.com/acme/widget"); private {
			t.Error("GONOSUMDB is set and does not match — GOPRIVATE must not override it")
		}
		if key, private := goModuleIsPrivate("corp.internal/secret"); !private || key != "GONOSUMDB" {
			t.Errorf("want private via GONOSUMDB, got (%q, %v)", key, private)
		}
	})

	t.Run("GOPRIVATE applies when GONOSUMDB is unset", func(t *testing.T) {
		t.Setenv("GONOSUMDB", "")
		t.Setenv("GOPRIVATE", "corp.internal/*")
		key, private := goModuleIsPrivate("corp.internal/secret")
		if !private || key != "GOPRIVATE" {
			t.Errorf("want private via GOPRIVATE, got (%q, %v)", key, private)
		}
	})

	t.Run("neither set means public", func(t *testing.T) {
		publicModuleEnv(t)
		if _, private := goModuleIsPrivate("github.com/reliant-labs/forge/pkg"); private {
			t.Error("with no private patterns set, every module is publicly checkable")
		}
	})
}

// TestVerifyGoModule_NetworkErrorIsUnreachable mirrors the npm anti-flake guard.
func TestVerifyGoModule_NetworkErrorIsUnreachable(t *testing.T) {
	publicModuleEnv(t)
	f := &stubFetcher{responses: []stubResponse{{
		match: "sum.golang.org", err: errors.New("no such host"),
	}}}

	got := verifyGoModuleArtifact(context.Background(), f, "example.com/thing", release.Artifact{
		Kind: release.KindGoModule, Version: "v1.0.0", Integrity: "h1:x=",
	})
	if got.Status != verifyUnreachable {
		t.Fatalf("status = %v (%s), want UNREACHABLE", got.Status, got.Detail)
	}
}

// ── OCI ─────────────────────────────────────────────────────────────────────

// TestVerifyOCI_Verified: a 200 at the digest address is the whole check,
// because the digest is content-addressed.
func TestVerifyOCI_Verified(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "/manifests/sha256:", status: http.StatusOK, body: `{"schemaVersion":2}`,
	}}}

	// The artifact's NAME is its repository, host included: the key is the
	// address, so there is no URI to carry.
	got := verifyOCIArtifact(context.Background(), f, "ghcr.io/reliant-labs/control-plane", release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("a")},
	})
	if got.Status != verifyVerified {
		t.Fatalf("status = %v (%s), want VERIFIED", got.Status, got.Detail)
	}
	if !f.requested("ghcr.io/v2/reliant-labs/control-plane/manifests/sha256:") {
		t.Errorf("unexpected manifest URL; requested: %v", f.calls)
	}
}

// TestVerifyOCI_DigestAbsent: the registry has no manifest at the digest this
// release pins.
func TestVerifyOCI_DigestAbsent(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{{
		match: "/manifests/", status: http.StatusNotFound, body: `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`,
	}}}

	got := verifyOCIArtifact(context.Background(), f, "ghcr.io/reliant-labs/control-plane", release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("b")},
	})
	if got.Status != verifyFailed {
		t.Fatalf("status = %v (%s), want FAILED", got.Status, got.Detail)
	}
}

// TestVerifyOCI_AnonymousTokenDance: a registry answering 401 with a bearer
// challenge must be retried with an anonymously-issued pull token. This is
// what keeps public-image verification credential-free.
func TestVerifyOCI_AnonymousTokenDance(t *testing.T) {
	digest := sha("c")
	challenge := http.Header{}
	challenge.Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:reliant-labs/control-plane:pull"`)

	authorized := false
	f := &stubFetcher{}
	f.responses = []stubResponse{
		{match: "ghcr.io/token", status: http.StatusOK, body: `{"token":"anon-pull-token"}`},
		{match: "/manifests/" + digest, status: http.StatusUnauthorized, body: "", header: challenge},
	}
	// Serve 200 only once a token has been issued, so the test proves the
	// retry actually carries it rather than succeeding by accident.
	wrapped := fetcherFunc(func(ctx context.Context, req fetchRequest) (fetchResponse, error) {
		if strings.Contains(req.URL, "ghcr.io/token") {
			authorized = true
			return f.Fetch(ctx, req)
		}
		if authorized && req.Bearer == "anon-pull-token" {
			return fetchResponse{StatusCode: http.StatusOK, Body: []byte(`{"schemaVersion":2}`), Header: http.Header{}}, nil
		}
		return f.Fetch(ctx, req)
	})

	got := verifyOCIArtifact(context.Background(), wrapped, "ghcr.io/reliant-labs/control-plane", release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: digest},
	})
	if got.Status != verifyVerified {
		t.Fatalf("status = %v (%s), want VERIFIED after the anonymous token dance", got.Status, got.Detail)
	}
}

// TestVerifyOCI_PrivateRegistryIsUnreachable: a registry that will not serve
// anonymously is outside what a credential-free verifier can see. That is
// UNREACHABLE, not FAILED — the image may well exist.
func TestVerifyOCI_PrivateRegistryIsUnreachable(t *testing.T) {
	f := &stubFetcher{responses: []stubResponse{
		{match: "/token", status: http.StatusForbidden, body: `{"message":"denied"}`},
		{match: "/manifests/", status: http.StatusUnauthorized, body: "", header: func() http.Header {
			h := http.Header{}
			h.Set("WWW-Authenticate", `Bearer realm="https://private.example/token",service="private.example"`)
			return h
		}()},
	}}

	got := verifyOCIArtifact(context.Background(), f, "private.example/secret", release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("d")},
	})
	if got.Status != verifyUnreachable {
		t.Fatalf("a credential-demanding registry must be UNREACHABLE, not FAILED; got %v (%s)", got.Status, got.Detail)
	}
}

// TestVerifyOCI_NoRegistryIsUnverifiable: a bare image name with a digest but
// no registry host is not an address. Passing it would be a green check over
// something never contacted — and guessing Docker Hub for it would be a
// confident FAILURE against a registry the image was never pushed to.
func TestVerifyOCI_NoRegistryIsUnverifiable(t *testing.T) {
	f := &stubFetcher{}
	got := verifyOCIArtifact(context.Background(), f, "control-plane", release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("e")}, // key names no host
	})
	if got.Status != verifyUnverifiable {
		t.Fatalf("status = %v (%s), want UNVERIFIABLE", got.Status, got.Detail)
	}
	if len(f.calls) != 0 {
		t.Errorf("nothing to address means nothing to request; requested: %v", f.calls)
	}
}

// TestOCIManifestCoordinates pins the repository → host+repo split. The input
// is now ONE value — the artifact's key, which is its full repository — because
// a ledger artifact is keyed by the repository the workload declared, so the
// key IS the address and there is no second field to reconcile.
func TestOCIManifestCoordinates(t *testing.T) {
	cases := []struct {
		repository string
		wantHost   string
		wantRepo   string
	}{
		{"ghcr.io/reliant-labs/control-plane", "ghcr.io", "reliant-labs/control-plane"},
		{"ghcr.io/control-plane", "ghcr.io", "control-plane"},
		{"us-central1-docker.pkg.dev/proj/repo/api", "us-central1-docker.pkg.dev", "proj/repo/api"},
		{"localhost:5000/api", "localhost:5000", "api"},
		{"docker.io/acme/api", "registry-1.docker.io", "acme/api"},
		// An unqualified official image lives under `library`.
		{"docker.io/alpine", "registry-1.docker.io", "library/alpine"},
		// A bare name is NOT an address. Guessing Docker Hub for it would
		// report a confident failure against a registry the image was never
		// pushed to, so it resolves to nothing and verify says unverifiable.
		{"api", "", ""},
		{"acme/api", "", ""},
	}
	for _, c := range cases {
		host, repo := ociManifestCoordinates(c.repository)
		if host != c.wantHost || repo != c.wantRepo {
			t.Errorf("ociManifestCoordinates(%q) = (%q, %q), want (%q, %q)",
				c.repository, host, repo, c.wantHost, c.wantRepo)
		}
	}
}

// TestParseAuthChallenge covers the quoting rule that matters: a scope value
// can contain commas, so splitting on every comma would truncate it.
func TestParseAuthChallenge(t *testing.T) {
	got := parseAuthChallenge(`realm="https://ghcr.io/token",service="ghcr.io",scope="repository:a/b:pull,push"`)
	if got["realm"] != "https://ghcr.io/token" {
		t.Errorf("realm = %q", got["realm"])
	}
	if got["scope"] != "repository:a/b:pull,push" {
		t.Errorf("scope must survive its internal comma, got %q", got["scope"])
	}
}

// ── Files ───────────────────────────────────────────────────────────────────

// TestVerifyFile_AlwaysUnverifiable pins the honest third state. A file
// artifact records a local sha256 and no publish destination, so there is
// nothing to fetch. Reporting it VERIFIED on the strength of a hash of bytes
// that never left the build machine would recreate the exact defect this
// command exists to catch.
func TestVerifyFile_AlwaysUnverifiable(t *testing.T) {
	got := verifyFileArtifact("forge-darwin-arm64", release.Artifact{
		Kind: release.KindFile, Version: "darwin-arm64", Integrity: sha("f"), // URI empty, as harvest writes it
	})
	if got.Status != verifyUnverifiable {
		t.Fatalf("status = %v (%s), want UNVERIFIABLE", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "URI") {
		t.Errorf("detail should name the missing publish destination: %q", got.Detail)
	}
}

// ── Dispatch, ordering, tallies ─────────────────────────────────────────────

// TestVerifyOneArtifact_UnknownKindIsUnverifiable: kinds are open so a newer
// forge's ledger round-trips through an older binary. Meeting an unknown kind
// teaches an old binary nothing, so it must not claim a verdict.
func TestVerifyOneArtifact_UnknownKindIsUnverifiable(t *testing.T) {
	got := verifyOneArtifact(context.Background(), &stubFetcher{}, "thing", release.Artifact{
		Kind: "cargo", Version: "1.0.0", Integrity: "x",
	})
	if got.Status != verifyUnverifiable {
		t.Fatalf("status = %v (%s), want UNVERIFIABLE", got.Status, got.Detail)
	}
}

// TestVerifyReleaseArtifacts_MixedLedgerSortedAndPerArtifact is the
// integration shape: a ledger holding every kind, checked concurrently, must
// produce one verdict per artifact SORTED BY NAME — and one failure among
// passes must remain individually visible. A summary line that hid it would
// defeat the command.
func TestVerifyReleaseArtifacts_MixedLedgerSortedAndPerArtifact(t *testing.T) {
	const goodIntegrity = "sha512-good"
	rel := release.Release{
		Version: "v1.4.0",
		Artifacts: map[string]release.Artifact{
			"zz.example/acme/image": {Kind: release.KindOCI, Mode: release.ModeShared,
				Digests: map[string]string{release.SharedVariant: sha("a")}},
			"aa-package":     {Kind: release.KindNPM, Version: "1.0.0", Integrity: goodIntegrity},
			"mm-unpublished": {Kind: release.KindNPM, Version: "2.0.0", Integrity: "sha512-never-shipped"},
			"bb-binary":      {Kind: release.KindFile, Version: "darwin-arm64", Integrity: sha("f")},
		},
	}

	f := &stubFetcher{responses: []stubResponse{
		{match: "/manifests/", status: http.StatusOK, body: `{"schemaVersion":2}`},
		{match: "aa-package", status: http.StatusOK, body: npmPackumentJSON(map[string]string{"1.0.0": goodIntegrity})},
		// The unpublished one: registry has 1.0.0 only.
		{match: "mm-unpublished", status: http.StatusOK, body: npmPackumentJSON(map[string]string{"1.0.0": "sha512-x"})},
	}}

	results := verifyReleaseArtifacts(context.Background(), f, rel, 4)

	if len(results) != 4 {
		t.Fatalf("want one verdict per artifact, got %d", len(results))
	}
	wantOrder := []string{"aa-package", "bb-binary", "mm-unpublished", "zz.example/acme/image"}
	for i, want := range wantOrder {
		if results[i].Name != want {
			t.Errorf("results[%d].Name = %q, want %q (output must be sorted, not concurrency-ordered)",
				i, results[i].Name, want)
		}
	}

	byName := map[string]artifactVerification{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if byName["aa-package"].Status != verifyVerified {
		t.Errorf("aa-package: %v (%s)", byName["aa-package"].Status, byName["aa-package"].Detail)
	}
	if byName["zz.example/acme/image"].Status != verifyVerified {
		t.Errorf("zz-image: %v (%s)", byName["zz.example/acme/image"].Status, byName["zz.example/acme/image"].Detail)
	}
	if byName["bb-binary"].Status != verifyUnverifiable {
		t.Errorf("bb-binary: %v (%s)", byName["bb-binary"].Status, byName["bb-binary"].Detail)
	}
	// The headline: one bad artifact among good ones stays visible.
	if byName["mm-unpublished"].Status != verifyFailed {
		t.Errorf("mm-unpublished must FAIL: %v (%s)", byName["mm-unpublished"].Status, byName["mm-unpublished"].Detail)
	}

	tally := tallyVerifications(results)
	want := verifyTally{Verified: 2, Failed: 1, Unverifiable: 1}
	if tally != want {
		t.Errorf("tally = %+v, want %+v", tally, want)
	}
}

// TestVerifyReleaseArtifacts_DeterministicUnderConcurrency runs the same
// ledger repeatedly: concurrency must never reach the report, or the command
// is not diffable in CI.
func TestVerifyReleaseArtifacts_DeterministicUnderConcurrency(t *testing.T) {
	rel := release.Release{Version: "v1", Artifacts: map[string]release.Artifact{}}
	for _, n := range []string{"e", "c", "a", "d", "b", "f", "g", "h"} {
		rel.Artifacts[n] = release.Artifact{Kind: release.KindFile, Integrity: sha("a")}
	}

	var first []string
	for run := 0; run < 8; run++ {
		results := verifyReleaseArtifacts(context.Background(), &stubFetcher{}, rel, 8)
		names := make([]string, len(results))
		for i, r := range results {
			names[i] = r.Name
		}
		if run == 0 {
			first = names
			continue
		}
		if strings.Join(names, ",") != strings.Join(first, ",") {
			t.Fatalf("output order varies between runs: %v vs %v", first, names)
		}
	}
	if strings.Join(first, ",") != "a,b,c,d,e,f,g,h" {
		t.Errorf("want alphabetical order, got %v", first)
	}
}

// ── Exit codes ──────────────────────────────────────────────────────────────

// TestExitCodeError_DistinguishesFailureFromUnreachable pins the decision that
// keeps this usable as a CI gate: a missing artifact (1) and an unreachable
// registry (2) must not share an exit code, or a team cannot tolerate network
// flake without also tolerating a broken release.
func TestExitCodeError_DistinguishesFailureFromUnreachable(t *testing.T) {
	var coded interface{ ExitCode() int }

	failure := error(exitCodeError{code: 1, msg: "artifact missing"})
	if !errors.As(failure, &coded) || coded.ExitCode() != 1 {
		t.Errorf("a verification failure should exit 1")
	}
	unreachable := error(exitCodeError{code: 2, msg: "registry unreachable"})
	if !errors.As(unreachable, &coded) || coded.ExitCode() != 2 {
		t.Errorf("an unreachable registry should exit 2, distinct from a real failure")
	}
}

// fetcherFunc adapts a function to httpFetcher, for tests that need per-call
// behaviour the table-driven stub cannot express.
type fetcherFunc func(context.Context, fetchRequest) (fetchResponse, error)

func (fn fetcherFunc) Fetch(ctx context.Context, req fetchRequest) (fetchResponse, error) {
	return fn(ctx, req)
}
