package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"

	"github.com/reliant-labs/forge/pkg/release"
)

// Release verification: proving a ledger's claims against the public world.
//
// A release ledger NAMES artifacts. Until this file, nothing checked that the
// named thing exists. That gap is not theoretical: forge v0.1.12 tagged
// `web-runtime/v0.3.1`, never published it to npm, and the ledger recorded a
// version alongside an integrity hash that no registry had ever served. The
// defect surfaced days later as a scaffolded project failing to install.
//
// Two properties make this trustworthy, and both are constraints on the
// design rather than features of it:
//
// NO CREDENTIALS. Every check reads a PUBLIC registry anonymously — npmjs.org,
// sum.golang.org, and an OCI registry's anonymous pull token. Nothing here
// reads a forge account, a hosted store, or a login. That is deliberate: the
// moment proof depends on a service we operate, "is this release real" becomes
// a question only we can answer, and the answer stops being independently
// checkable by the person who most needs it. An artifact's existence is a fact
// about the public internet, and it is verified against the public internet.
//
// NOTHING IS SILENTLY PASSED. A check that could not run reports that it could
// not run. See verifyStatus — the three-state model is the whole point, and
// collapsing it to pass/fail is the specific way a verifier becomes worse than
// no verifier at all.

// verifyStatus is the outcome of checking ONE artifact.
//
// THREE OUTCOMES, NEVER TWO. A boolean verifier has to file everything it
// could not check under one of "passed" or "failed", and both choices are
// lies with consequences. Filing it under passed means a green run that
// proves nothing — exactly the state v0.1.12 shipped in. Filing it under
// failed means the command is red for conditions nobody can fix (a file
// artifact has no publish destination because forge never records one), and a
// gate that is permanently red gets deleted from CI within a week.
//
// So "could not check" is its own answer, and it is further split by CAUSE,
// because the two causes call for opposite responses: a STRUCTURAL gap is a
// property of the ledger that will still be there tomorrow, while an
// UNREACHABLE network is transient and says nothing whatsoever about the
// artifact. Conflating them is what turns a verifier into a flaky gate.
type verifyStatus int

const (
	// verifyVerified: the artifact exists and its bytes match what the
	// ledger recorded.
	verifyVerified verifyStatus = iota
	// verifyFailed: the artifact is PROVEN wrong — absent from the
	// registry, or present with a different hash. This is the only status
	// that indicts the release.
	verifyFailed
	// verifyUnverifiable: a structural gap makes the check impossible.
	// The ledger did not record enough to check (a file artifact with no
	// URI), or the artifact is outside the public checkable world (a
	// GOPRIVATE module). Says nothing about the artifact's validity.
	verifyUnverifiable
	// verifyUnreachable: the check could not COMPLETE — a timeout, a DNS
	// failure, a registry demanding credentials. Distinct from
	// verifyUnverifiable because it is transient: the same ledger checked
	// again on a working network may well verify.
	verifyUnreachable
)

// String renders the status as the fixed-width label used in the report.
func (s verifyStatus) String() string {
	switch s {
	case verifyVerified:
		return "VERIFIED"
	case verifyFailed:
		return "FAILED"
	case verifyUnverifiable:
		return "UNVERIFIABLE"
	case verifyUnreachable:
		return "UNREACHABLE"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON emits the lowercase string form so `forge release verify --json`
// is readable by a human and by jq, rather than emitting the iota. The four
// outcomes stay four values on the wire: a consumer that folds "unverifiable"
// into "verified" is asserting something the report never claims.
func (s verifyStatus) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ToLower(s.String()) + `"`), nil
}

// UnmarshalJSON is the inverse, so the report round-trips through Go. An
// unrecognized label is an ERROR rather than the zero value: the zero value is
// verifyVerified, so silently accepting a status this build does not know
// would decode an unreadable verdict as a passing one — the exact failure the
// four-outcome split exists to prevent.
func (s *verifyStatus) UnmarshalJSON(b []byte) error {
	var label string
	if err := json.Unmarshal(b, &label); err != nil {
		return fmt.Errorf("verify status must be a string: %w", err)
	}
	switch strings.ToLower(label) {
	case "verified":
		*s = verifyVerified
	case "failed":
		*s = verifyFailed
	case "unverifiable":
		*s = verifyUnverifiable
	case "unreachable":
		*s = verifyUnreachable
	default:
		return fmt.Errorf("unknown verify status %q", label)
	}
	return nil
}

// artifactVerification is one artifact's verdict. Detail carries the SPECIFIC
// reason — which hash differed, which versions the registry does have, which
// URL timed out — because a report that says only "FAILED" sends the reader
// back to do the investigation the command just did.
type artifactVerification struct {
	Name   string       `json:"name"`
	Kind   string       `json:"kind"`
	Status verifyStatus `json:"status"`
	Detail string       `json:"detail,omitempty"`
}

// ── HTTP seam ───────────────────────────────────────────────────────────────

// fetchRequest is one outbound GET. Bearer is set only on the retry of an OCI
// manifest read, after the registry's anonymous token endpoint has issued a
// token (see verifyOCIArtifact).
type fetchRequest struct {
	URL    string
	Accept string
	Bearer string
}

// fetchResponse is the part of an HTTP response verification reads. Header is
// needed for exactly one thing — the OCI `WWW-Authenticate` challenge that
// names a registry's anonymous token endpoint.
type fetchResponse struct {
	StatusCode int
	Body       []byte
	Header     http.Header
}

// httpFetcher is the ONE network capability verification needs, declared here
// at the consumer rather than exported from a transport package.
//
// It exists as a seam so the unit tests can exercise every branch — hash
// mismatch, absent version, timeout — WITHOUT touching the network. A test
// that reaches the real npm registry is not a unit test: it fails when the
// network is slow, passes when the code is broken and the registry happens to
// agree, and cannot construct the interesting cases at all (there is no way to
// make npmjs.org serve a corrupted hash on demand).
//
// An error return means the request did not complete — the transport failed.
// A non-2xx StatusCode means it completed and the server said no. Those are
// different facts and the two return paths keep them from being confused.
type httpFetcher interface {
	Fetch(ctx context.Context, req fetchRequest) (fetchResponse, error)
}

// httpClientFetcher is the production httpFetcher, over net/http.
type httpClientFetcher struct {
	client *http.Client
}

// maxVerifyBodyBytes caps a response read. An npm packument for a package with
// thousands of versions is large but bounded; this stops a hostile or broken
// endpoint from exhausting memory while leaving real metadata untruncated.
const maxVerifyBodyBytes = 16 << 20

func (f httpClientFetcher) Fetch(ctx context.Context, req fetchRequest) (fetchResponse, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return fetchResponse{}, err
	}
	if req.Accept != "" {
		hreq.Header.Set("Accept", req.Accept)
	}
	if req.Bearer != "" {
		hreq.Header.Set("Authorization", "Bearer "+req.Bearer)
	}
	// Identifies the client to registries that log or rate-limit by agent,
	// and makes a forge verification run recognisable in a registry's logs.
	hreq.Header.Set("User-Agent", "forge-release-verify")

	resp, err := f.client.Do(hreq)
	if err != nil {
		return fetchResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVerifyBodyBytes))
	if err != nil {
		// A read that dies mid-body is a transport failure, not a verdict.
		return fetchResponse{}, err
	}
	return fetchResponse{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}, nil
}

// newHTTPFetcher builds the production fetcher with a per-request timeout.
func newHTTPFetcher(timeout time.Duration) httpFetcher {
	return httpClientFetcher{client: &http.Client{Timeout: timeout}}
}

// ── Registry endpoints ──────────────────────────────────────────────────────

const (
	// npmRegistryBase is the public npm registry. Reads here are
	// unauthenticated for public packages.
	npmRegistryBase = "https://registry.npmjs.org"
	// npmAbbreviatedAccept asks npm for the "install" projection of a
	// packument: the same version list and the same dist.integrity, at a
	// fraction of the bytes (measured on forge's own web-runtime: 4.7KB
	// versus 20KB). Integrity is what we compare, and it is present in
	// both, so the smaller document is strictly better.
	npmAbbreviatedAccept = "application/vnd.npm.install-v1+json"

	// goChecksumDBBase is the public Go checksum database.
	//
	// WHY NOT proxy.golang.org/<mod>/@v/<version>.info, THE OBVIOUS CHOICE.
	// The proxy caches NEGATIVE results, so anything that polls a version
	// before its tag is pushed — CI, a release script, an impatient human —
	// leaves behind a 404 that outlives the tag's creation. We have hit that
	// repeatedly, and a verifier that reports "not published" for a module
	// that IS published is worse than useless: it trains people to ignore it.
	// The checksum DB is an append-only transparency log with no negative
	// caching, and it returns the go.sum lines directly, so ONE request
	// answers both "does this version exist" and "does the hash match".
	goChecksumDBBase = "https://sum.golang.org"

	// ociManifestAccept lists every manifest media type a digest might
	// address. A registry content-negotiates on this, and omitting a type
	// makes it answer 404 for a manifest that exists.
	ociManifestAccept = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
)

// ── The verifier ────────────────────────────────────────────────────────────

// verifyReleaseArtifacts checks every artifact in a release and returns one
// verdict per artifact, SORTED BY NAME.
//
// Checks run concurrently because they are independent network reads and a
// release with twenty artifacts should not take twenty round-trips of
// wall-clock. Output order is fixed by sorting the names up front and writing
// each result into its own slot, so concurrency never reaches the report: two
// runs over the same ledger produce byte-identical output, which is what makes
// the command diffable in CI.
func verifyReleaseArtifacts(ctx context.Context, f httpFetcher, rel release.Release, concurrency int) []artifactVerification {
	// Snapshot catalogs are content-addressed, so the verifier's work key must
	// be the artifact ID, not repository (which may have two active digests).
	artifacts := rel.Artifacts
	if len(rel.Catalog) > 0 {
		artifacts = map[string]release.Artifact{}
		for id, a := range rel.Catalog {
			converted := release.Artifact{Kind: a.Kind, Platforms: a.Platforms, URI: a.Location}
			if a.Kind == release.KindOCI {
				converted.Digests = map[string]string{"": a.Content}
			} else if a.Kind == release.KindGit {
				converted.Source = &release.Source{Repo: a.Location, Commit: a.Content}
			} else {
				converted.Integrity = a.Content
			}
			artifacts[string(id)] = converted
		}
	}
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]artifactVerification, len(names))
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, name := range names {
		wg.Add(1)
		go func(idx int, artName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = verifyOneArtifact(ctx, f, artName, artifacts[artName])
		}(i, name)
	}
	wg.Wait()
	return results
}

// verifyOneArtifact dispatches on kind. An UNKNOWN kind is unverifiable rather
// than failed: kinds are open by design so a newer forge's ledger round-trips
// through an older one, and an older binary meeting a kind it has never heard
// of has learned nothing about whether that artifact is valid.
func verifyOneArtifact(ctx context.Context, f httpFetcher, name string, art release.Artifact) artifactVerification {
	kind := string(art.Kind)
	switch art.Kind {
	case release.KindNPM:
		return verifyNPMArtifact(ctx, f, name, art)
	case release.KindGoModule:
		return verifyGoModuleArtifact(ctx, f, name, art)
	case release.KindOCI:
		return verifyOCIArtifact(ctx, f, name, art)
	case release.KindFile:
		return verifyFileArtifact(name, art)
	case release.KindGit:
		// A source pin names a commit in a repository, not bytes in a
		// registry. There is no public artifact to fetch, so the honest
		// verdict is UNVERIFIABLE rather than a pass.
		return artifactVerification{
			Name: name, Kind: kind, Status: verifyUnverifiable,
			Detail: "a source-pinned artifact is built at deploy time from its commit; there is no published artifact to check",
		}
	default:
		return artifactVerification{
			Name: name, Kind: kind, Status: verifyUnverifiable,
			Detail: fmt.Sprintf("unknown artifact kind %q — this forge does not know how to check it", kind),
		}
	}
}

// ── npm ─────────────────────────────────────────────────────────────────────

// npmPackument is the subset of npm's registry metadata verification reads.
type npmPackument struct {
	Versions map[string]struct {
		Dist struct {
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	} `json:"versions"`
}

// verifyNPMArtifact checks that the registry has this exact version and that
// its published integrity hash equals the one the ledger recorded.
//
// THIS IS THE v0.1.12 CHECK. That release named `@reliantlabs/forge-web-runtime`
// at 0.3.1 with an integrity hash taken from `npm pack` — bytes that existed
// on the build machine and nowhere else, because the publish never ran. The
// version lookup below is what turns that into a hard failure at release time
// instead of an install failure in a stranger's terminal.
//
// A hash MISMATCH is treated as failure rather than a warning, and it is the
// more serious of the two findings: it means bytes DIFFERENT from the ones
// this release built were published under a version number that is now
// permanently taken. npm forbids republishing a version, so this is not
// self-correcting — it requires a new version, and knowing that immediately is
// the difference between one bad version and a cascade.
func verifyNPMArtifact(ctx context.Context, f httpFetcher, name string, art release.Artifact) artifactVerification {
	res := artifactVerification{Name: name, Kind: string(release.KindNPM)}

	if art.Version == "" {
		res.Status = verifyUnverifiable
		res.Detail = "ledger records no version — nothing to look up"
		return res
	}

	base := npmRegistryBase
	if art.URI != "" {
		base = strings.TrimSuffix(art.URI, "/")
	}
	// PathEscape encodes the '/' in a scoped name (@scope/pkg) as %2F, which
	// is the spelling npm's own clients use and which the registry accepts.
	endpoint := base + "/" + url.PathEscape(name)

	resp, err := f.Fetch(ctx, fetchRequest{URL: endpoint, Accept: npmAbbreviatedAccept})
	if err != nil {
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s: %v", endpoint, err)
		return res
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("package is not published — %s returned 404 for the package itself", base)
		return res
	case resp.StatusCode != http.StatusOK:
		// A 5xx or a rate limit is the registry having a bad day, not a
		// verdict on the artifact.
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s returned HTTP %d", endpoint, resp.StatusCode)
		return res
	}

	var doc npmPackument
	if jerr := json.Unmarshal(resp.Body, &doc); jerr != nil {
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("could not parse registry metadata from %s: %v", endpoint, jerr)
		return res
	}

	entry, ok := doc.Versions[art.Version]
	if !ok {
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("version %s is NOT published (registry has: %s)",
			art.Version, summarizeVersions(doc.Versions))
		return res
	}

	if art.Integrity == "" {
		// Presence was confirmed; the bytes were not. Reporting this green
		// would claim a comparison that never happened.
		res.Status = verifyUnverifiable
		res.Detail = fmt.Sprintf("version %s is published, but the ledger records no integrity hash — bytes not compared", art.Version)
		return res
	}
	if entry.Dist.Integrity != art.Integrity {
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("integrity MISMATCH for %s\n      ledger:   %s\n      registry: %s",
			art.Version, art.Integrity, entry.Dist.Integrity)
		return res
	}

	res.Status = verifyVerified
	res.Detail = fmt.Sprintf("%s published, integrity matches registry", art.Version)
	return res
}

// summarizeVersions renders the versions a registry DOES have, so the report
// answers the reader's immediate next question ("then what did ship?") without
// a second command. Capped because a long-lived package has hundreds and a
// wall of them buries the finding.
func summarizeVersions[T any](versions map[string]T) string {
	if len(versions) == 0 {
		return "none"
	}
	all := make([]string, 0, len(versions))
	for v := range versions {
		all = append(all, v)
	}
	sort.Strings(all)
	const maxShown = 5
	if len(all) <= maxShown {
		return strings.Join(all, ", ")
	}
	// The newest versions are the informative ones — show the tail.
	return fmt.Sprintf("%d versions, latest: %s",
		len(all), strings.Join(all[len(all)-maxShown:], ", "))
}

// ── Go modules ──────────────────────────────────────────────────────────────

// verifyGoModuleArtifact checks the public checksum database for this module
// version and compares the module ZIP hash against the ledger's.
//
// THE HASH COMPARED IS THE ZIP HASH, NOT THE go.mod HASH. sum.golang.org
// returns two lines per module: `<path> <version> h1:…` hashes the module's
// CONTENT, and `<path> <version>/go.mod h1:…` hashes its go.mod alone. They
// are different bytes and never equal. Harvesting deliberately indexes only
// the first (see goSumHashes), and picking the wrong line here would compare a
// hash of the content against a hash of the manifest and report a mismatch on
// a perfectly good release — a false alarm that would discredit the command.
//
// A GOPRIVATE module is reported unverifiable rather than failed. The checksum
// DB has no record of it BY DESIGN, so its 404 means "not my department", not
// "does not exist" — and treating that as failure would make this command
// unusable for anyone with a private module in their release.
func verifyGoModuleArtifact(ctx context.Context, f httpFetcher, name string, art release.Artifact) artifactVerification {
	res := artifactVerification{Name: name, Kind: string(release.KindGoModule)}

	if art.Version == "" {
		res.Status = verifyUnverifiable
		res.Detail = "ledger records no version — nothing to look up"
		return res
	}
	if privatePattern, private := goModuleIsPrivate(name); private {
		res.Status = verifyUnverifiable
		res.Detail = fmt.Sprintf("module matches %s — the public checksum database has no record of private modules", privatePattern)
		return res
	}

	escPath, err := module.EscapePath(name)
	if err != nil {
		res.Status = verifyUnverifiable
		res.Detail = fmt.Sprintf("not a valid module path: %v", err)
		return res
	}
	escVer, err := module.EscapeVersion(art.Version)
	if err != nil {
		res.Status = verifyUnverifiable
		res.Detail = fmt.Sprintf("not a valid module version: %v", err)
		return res
	}
	endpoint := fmt.Sprintf("%s/lookup/%s@%s", goChecksumDBBase, escPath, escVer)

	resp, err := f.Fetch(ctx, fetchRequest{URL: endpoint})
	if err != nil {
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s: %v", endpoint, err)
		return res
	}
	switch {
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("version %s is NOT in the checksum database — no such module version has ever been fetched from the public proxy", art.Version)
		return res
	case resp.StatusCode != http.StatusOK:
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s returned HTTP %d", endpoint, resp.StatusCode)
		return res
	}

	published, ok := goSumZipHashFromLookup(string(resp.Body), name, art.Version)
	if !ok {
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("checksum database response for %s@%s carried no module hash line", name, art.Version)
		return res
	}

	if art.Integrity == "" {
		res.Status = verifyUnverifiable
		res.Detail = fmt.Sprintf("version %s exists, but the ledger records no h1: hash — bytes not compared", art.Version)
		return res
	}
	if published != art.Integrity {
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("module hash MISMATCH for %s\n      ledger:      %s\n      checksum DB: %s",
			art.Version, art.Integrity, published)
		return res
	}

	res.Status = verifyVerified
	res.Detail = fmt.Sprintf("%s present, h1: hash matches the checksum database", art.Version)
	return res
}

// goSumZipHashFromLookup extracts the module ZIP hash from a sum.golang.org
// /lookup response, whose body is a signed tree containing the same lines a
// go.sum holds:
//
//	<sequence number>
//	<path> <version> h1:…
//	<path> <version>/go.mod h1:…
//	<blank>
//	go.sum database tree
//	…signature…
//
// Matching on the exact "<path> <version>" pair is what excludes the adjacent
// "/go.mod" line without a special case: that line's version field carries the
// suffix, so it simply does not match.
func goSumZipHashFromLookup(body, modPath, version string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if fields[0] != modPath || fields[1] != version {
			continue
		}
		if !strings.HasPrefix(fields[2], "h1:") {
			continue
		}
		return fields[2], true
	}
	return "", false
}

// goModuleIsPrivate reports whether a module path is excluded from the public
// checksum database by this environment's settings, and which variable matched.
//
// PRECEDENCE MIRRORS THE GO COMMAND. GONOSUMDB is the specific control over
// what is checked against the checksum database; GOPRIVATE is the umbrella
// that supplies its default when it is unset. So GONOSUMDB wins when present,
// exactly as `go env` resolves them — a machine that has narrowed or widened
// the sumdb rule specifically should not have GOPRIVATE silently override it.
//
// WHY HONOR THESE AT ALL. Sending a private module path to sum.golang.org
// leaks the path to a public service, which is the precise thing GOPRIVATE
// exists to prevent. A verifier that ignored it would exfiltrate internal
// module names as a side effect of checking a release.
//
// WHAT THIS DELIBERATELY DOES NOT DO: skip quietly. A skipped module is
// reported UNVERIFIABLE with the variable that caused it named in the detail
// line, so a blanket `GONOSUMDB=*` — which is usually an inherited workaround
// rather than a statement that every module is private — shows up as a wall of
// UNVERIFIABLE rows naming GONOSUMDB, not as a green run. That is the whole
// reason the third state exists.
//
// Reading the environment directly rather than shelling out to `go env` keeps
// verification free of a subprocess; these are the documented variables the go
// command itself consults.
func goModuleIsPrivate(modPath string) (string, bool) {
	keys := []string{"GONOSUMDB", "GOPRIVATE"}
	for _, key := range keys {
		patterns := os.Getenv(key)
		if patterns == "" {
			continue
		}
		if module.MatchPrefixPatterns(patterns, modPath) {
			return key, true
		}
		// GONOSUMDB is set but does not match: it is authoritative for the
		// sumdb decision, so GOPRIVATE's broader pattern must not override
		// it. This mirrors the go command, where GOPRIVATE only supplies a
		// DEFAULT for an unset GONOSUMDB.
		return "", false
	}
	return "", false
}

// ── OCI ─────────────────────────────────────────────────────────────────────

// verifyOCIArtifact checks that the registry serves a manifest at the recorded
// digest.
//
// EXISTENCE IS THE WHOLE CHECK, and that is not a shortcut. An OCI digest is
// content-addressed: the registry computes it from the manifest bytes and
// refuses to serve anything else at that address. So a 200 from
// `/v2/<repo>/manifests/sha256:…` already proves the bytes are the bytes —
// there is no second hash to compare, unlike npm and Go where the coordinate
// (a version string) is mutable and the hash is a separate claim.
//
// Anonymous pull only. A registry that answers the token challenge without
// credentials is verified; one that demands a login is reported UNREACHABLE,
// never failed. Requiring a credential to verify would defeat the property
// this command exists to have — that anyone can check a release without our
// permission — so a private registry is honestly out of scope rather than
// quietly half-checked.
func verifyOCIArtifact(ctx context.Context, f httpFetcher, name string, art release.Artifact) artifactVerification {
	res := artifactVerification{Name: name, Kind: string(release.KindOCI)}

	digest, ok := art.SharedDigest()
	if !ok {
		res.Status = verifyUnverifiable
		res.Detail = "no shared digest recorded (variant-mode artifacts are not yet resolvable)"
		return res
	}
	// The artifact's NAME is its repository, registry host included, because
	// that is how harvestReleaseArtifacts keys an OCI artifact. So the address
	// needs no second field — but a ledger entry with no host is still
	// unverifiable (a local/compose build that pushed nowhere), and says so
	// rather than guessing Docker Hub and reporting a spurious failure.
	location := name
	if art.URI != "" {
		location = art.URI
	}
	host, repo := ociManifestCoordinates(location)
	if host == "" {
		res.Status = verifyUnverifiable
		res.Detail = bareRepositoryDetail(name, digest)
		return res
	}

	endpoint := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, digest)

	resp, err := f.Fetch(ctx, fetchRequest{URL: endpoint, Accept: ociManifestAccept})
	if err != nil {
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s: %v", endpoint, err)
		return res
	}

	// A registry answers an unauthenticated read with 401 plus a challenge
	// naming its token endpoint. Public repositories issue a pull token to
	// anyone who asks, so completing the dance is still credential-free.
	if resp.StatusCode == http.StatusUnauthorized {
		token, terr := ociAnonymousToken(ctx, f, resp.Header.Get("WWW-Authenticate"))
		if terr != nil {
			res.Status = verifyUnreachable
			res.Detail = fmt.Sprintf("%s requires credentials: %v", host, terr)
			return res
		}
		resp, err = f.Fetch(ctx, fetchRequest{URL: endpoint, Accept: ociManifestAccept, Bearer: token})
		if err != nil {
			res.Status = verifyUnreachable
			res.Detail = fmt.Sprintf("GET %s: %v", endpoint, err)
			return res
		}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		res.Status = verifyVerified
		res.Detail = fmt.Sprintf("%s serves a manifest at %s", host, digest)
		return res
	case resp.StatusCode == http.StatusNotFound:
		res.Status = verifyFailed
		res.Detail = fmt.Sprintf("no manifest at %s in %s/%s — the digest this release pins is not in the registry", digest, host, repo)
		return res
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("%s/%s is not anonymously readable (HTTP %d) — verification does not use credentials", host, repo, resp.StatusCode)
		return res
	default:
		res.Status = verifyUnreachable
		res.Detail = fmt.Sprintf("GET %s returned HTTP %d", endpoint, resp.StatusCode)
		return res
	}
}

// ociManifestCoordinates splits a recorded registry value and an image name
// into the API host and the repository path.
//
// A forge registry value is what the user passed to `--push`: either a bare
// host ("ghcr.io"), a host plus namespace ("ghcr.io/reliant-labs"), or a
// Docker Hub namespace with no host at all ("acme"). The first segment is a
// HOST when it looks like one — it contains a dot or a port, or it is
// localhost — because a DNS name and a Docker Hub username are otherwise
// indistinguishable, and that heuristic is the same one docker itself uses.
// ociManifestCoordinates splits a full repository (`ghcr.io/acme/shop`) into
// the registry API host and the repository path to request a manifest from.
// Returns ("", "") when the reference names no host — a bare name is not an
// address, and guessing Docker Hub for it would report a confident failure
// against a registry the image was never pushed to.
func ociManifestCoordinates(repository string) (host, repo string) {
	repository = strings.Trim(repository, "/")
	first, rest, ok := strings.Cut(repository, "/")
	if !ok || !(strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		return "", ""
	}
	host, repo = first, rest

	// Docker Hub's registry API lives on a different name than its pull
	// alias, and an unqualified official image sits under the `library`
	// namespace.
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	return host, repo
}

// ociTokenResponse is a registry token endpoint's reply. Registries disagree
// on the field name — the OAuth2 spelling is `access_token`, the older Docker
// spelling is `token` — so both are read and either satisfies the request.
type ociTokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

// ociAnonymousToken completes a registry's Bearer challenge WITHOUT
// credentials, which public repositories permit.
//
// The challenge looks like:
//
//	Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:org/img:pull"
//
// The realm is fetched with service and scope forwarded as query parameters.
// A registry that will not issue an anonymous token returns an error here, and
// the caller reports UNREACHABLE — the artifact is simply outside what a
// credential-free verifier can see.
func ociAnonymousToken(ctx context.Context, f httpFetcher, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("registry did not offer a bearer challenge")
	}
	params := parseAuthChallenge(challenge[len("Bearer "):])
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("bearer challenge named no realm")
	}

	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	endpoint := realm
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}

	resp, err := f.Fetch(ctx, fetchRequest{URL: endpoint})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anonymous token request returned HTTP %d", resp.StatusCode)
	}
	var tok ociTokenResponse
	if jerr := json.Unmarshal(resp.Body, &tok); jerr != nil {
		return "", fmt.Errorf("could not parse token response: %w", jerr)
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	if tok.AccessToken != "" {
		return tok.AccessToken, nil
	}
	return "", fmt.Errorf("registry issued no anonymous token")
}

// parseAuthChallenge splits a `key="value",key="value"` challenge body. Values
// are comma-separated and quoted; a scope value itself contains commas only
// inside its quotes, so the split tracks quoting rather than cutting on every
// comma.
func parseAuthChallenge(s string) map[string]string {
	out := map[string]string{}
	var current strings.Builder
	inQuotes := false
	flush := func() {
		part := strings.TrimSpace(current.String())
		current.Reset()
		if part == "" {
			return
		}
		key, value, found := strings.Cut(part, "=")
		if !found {
			return
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// ── Files ───────────────────────────────────────────────────────────────────

// verifyFileArtifact reports on a published file.
//
// IT ALWAYS REPORTS UNVERIFIABLE TODAY, AND SAYING SO IS THE POINT. A file
// artifact records a sha256 of bytes that existed on the build machine, and
// nothing records WHERE those bytes were published — forge builds these
// binaries but never uploads them, and no KCL declaration names a destination
// (see harvestFileArtifacts). With no URI there is no request to make.
//
// The alternative — quietly counting it as verified because the local hash is
// present — is precisely the failure this command was built to prevent, just
// relocated: a green check over an artifact nobody confirmed exists anywhere.
// A third state costs a line of output and keeps the report honest until a
// publish destination is declared, at which point this function fetches the
// URI and compares.
func verifyFileArtifact(name string, art release.Artifact) artifactVerification {
	res := artifactVerification{Name: name, Kind: string(release.KindFile), Status: verifyUnverifiable}
	switch {
	case art.URI == "" && art.Integrity == "":
		res.Detail = "ledger records neither a publish destination nor a hash — nothing to check"
	case art.URI == "":
		res.Detail = fmt.Sprintf("%s recorded, but no publish destination (URI) — forge does not yet record where a file artifact is uploaded", art.Integrity)
	default:
		// Reachable only for a hand-written or future ledger. Downloading
		// and hashing an arbitrary URL is a real capability, and adding it
		// speculatively before anything WRITES a URI would ship an untested
		// path; when the producing side lands, this is where it goes.
		res.Detail = fmt.Sprintf("publish destination %s recorded, but downloading file artifacts is not implemented", art.URI)
	}
	return res
}

// ── Tallies ─────────────────────────────────────────────────────────────────

// verifyTally counts verdicts by status, for the summary line and the exit
// code decision.
type verifyTally struct {
	Verified     int `json:"verified"`
	Failed       int `json:"failed"`
	Unverifiable int `json:"unverifiable"`
	Unreachable  int `json:"unreachable"`
}

func tallyVerifications(results []artifactVerification) verifyTally {
	var t verifyTally
	for _, r := range results {
		switch r.Status {
		case verifyVerified:
			t.Verified++
		case verifyFailed:
			t.Failed++
		case verifyUnverifiable:
			t.Unverifiable++
		case verifyUnreachable:
			t.Unreachable++
		}
	}
	return t
}
