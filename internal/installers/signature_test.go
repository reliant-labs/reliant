package installers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// The release-signature contract (RF-2, 2026-11-01 security review): who may
// sign a forge release, what the attestation claims, and the asset it ships in.
// install.sh verifies with these values, release-attest.yml signs and
// self-checks with them, and release-npm.yml verifies with them before it pins
// checksums into the npm package. A drift between any two of them makes every
// install refuse a correctly signed release, or accept a wrongly signed one.
const (
	signatureAsset = "forge-release.sigstore.json"
	signerIdentity = `^https://github\.com/reliant-labs/forge/\.github/workflows/release-attest\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$`
	predicateType  = "https://github.com/reliant-labs/forge/attestations/release-assets/v1"
)

func TestSignatureContractIsSharedByInstallerAndWorkflows(t *testing.T) {
	for _, rel := range []string{"install.sh", ".github/workflows/release-attest.yml", ".github/workflows/release-npm.yml"} {
		body := readRepoFile(t, rel)
		for _, want := range []string{signatureAsset, signerIdentity, predicateType} {
			if !strings.Contains(body, want) {
				t.Errorf("%s does not carry %q", rel, want)
			}
		}
	}
	attest := strings.Join(codeLines(readRepoFile(t, ".github/workflows/release-attest.yml")), "\n")
	for _, want := range []string{"types: [published]", "id-token: write", "attestations: write", "uses: actions/attest@", "predicate-type:", "gh release upload", "FORGE_REQUIRE_SIGNATURE=1"} {
		if !strings.Contains(attest, want) {
			t.Errorf("release-attest.yml lacks %q", want)
		}
	}
	// Binding: a verifier must also check that the signed predicate names the
	// release, or an older release's signed set replays under a newer tag.
	for rel, body := range map[string]string{"release-attest.yml": attest, "release-npm.yml": strings.Join(codeLines(readRepoFile(t, ".github/workflows/release-npm.yml")), "\n")} {
		if !strings.Contains(body, ".verificationResult.statement.predicate.release == $release") {
			t.Errorf("%s does not bind the verified signature to the release", rel)
		}
	}
	if strings.Contains(attest, "--clobber") {
		t.Error("release-attest.yml must never replace a release's signature (--clobber)")
	}
	npm := strings.Join(codeLines(readRepoFile(t, ".github/workflows/release-npm.yml")), "\n")
	for _, want := range []string{"gh attestation verify", "npm/lib/release-checksums.txt", "package/lib/release-checksums.txt"} {
		if !strings.Contains(npm, want) {
			t.Errorf("release-npm.yml lacks %q", want)
		}
	}
	// The pin must be written after `npm test` (which exercises the unpinned
	// path) and before the tarball is packed.
	pin, test, pack := strings.Index(npm, "cp \"$dir/checksums.txt\" npm/lib/release-checksums.txt"), strings.Index(npm, "run: npm test"), strings.Index(npm, "npm pack")
	if pin < 0 || test < 0 || pack < 0 || pin < test || pin > pack {
		t.Errorf("release-npm.yml must pin checksums after `npm test` and before `npm pack` (test@%d pin@%d pack@%d)", test, pin, pack)
	}
}

// The identity admits the release workflow at a release tag or on main, and
// nothing else: not another branch (anyone with push access could run an
// edited copy there), not another workflow, not another repository.
func TestSignerIdentityAdmitsOnlyTheReleaseWorkflow(t *testing.T) {
	re := regexp.MustCompile(signerIdentity)
	const wf = "https://github.com/reliant-labs/forge/.github/workflows/release-attest.yml"
	for _, ok := range []string{wf + "@refs/tags/v1.0.14", wf + "@refs/tags/v1.1.0-rc.1", wf + "@refs/heads/main"} {
		if !re.MatchString(ok) {
			t.Errorf("identity refuses %s", ok)
		}
	}
	for _, bad := range []string{
		wf + "@refs/heads/attacker",
		wf + "@refs/heads/main-evil",
		wf + "@refs/tags/latest",
		wf + "@refs/pull/1/merge",
		"https://github.com/reliant-labs/forge/.github/workflows/ci.yml@refs/heads/main",
		"https://github.com/attacker/forge/.github/workflows/release-attest.yml@refs/heads/main",
		"https://github.com/reliant-labs/forge/.github/workflows/release-attest.yml@refs/tags/v1.0.0/x",
	} {
		if re.MatchString(bad) {
			t.Errorf("identity admits %s", bad)
		}
	}
}

// toolbox returns a directory holding only the commands install.sh and the
// fake verifiers need. Pointing PATH at it (plus a fake bin dir) keeps a real
// gh or cosign on this machine out of the test.
func toolbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"sh", "uname", "mktemp", "rm", "curl", "awk", "tr", "tar", "gzip", "mkdir", "cp", "chmod", "mv", "sed", "grep", "cat", "sha256sum", "base64", "wc"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeGh verifies like `gh attestation verify --bundle`: it succeeds only when
// the bundle's statement lists the file's sha256 and install.sh passed forge's identity,
// predicate type and repository. With noAttestation it is a gh older than
// 2.49, which has no attestation command.
const fakeGh = `#!/bin/sh
echo "gh $*" >>"$FAKE_LOG"
[ "$1 $2" = "attestation verify" ] || exit 64
shift 2
[ "$1" = "--help" ] && { [ -z "$FAKE_NO_ATTESTATION" ] || exit 1; exit 0; }
file=$1
shift
bundle= identity= ptype= repo=
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift ;;
    --cert-identity-regex) identity=$2; shift ;;
    --predicate-type) ptype=$2; shift ;;
    --repo) repo=$2; shift ;;
  esac
  shift
done
[ "$identity" = "$FAKE_IDENTITY" ] || { echo "wrong signer identity: $identity"; exit 1; }
[ "$ptype" = "$FAKE_PREDICATE" ] || { echo "wrong predicate type: $ptype"; exit 1; }
[ "$repo" = reliant-labs/forge ] || { echo "wrong repo: $repo"; exit 1; }
sum=$(sha256sum "$file" | awk '{print $1}')
statement=$(tr -d ' \n' <"$bundle" | grep -o '"payload":"[^"]*"' | sed 's/^"payload":"//; s/"$//' | base64 -d)
case "$statement" in *"\"sha256\":\"$sum\""*) ;; *) echo "Error: digest $sum is not a subject of the attestation"; exit 1 ;; esac
echo "Verification succeeded!"
`

// fakeCosign is cosign verify-blob-attestation. FAKE_COSIGN_V2 makes it a 2.x
// that needs --new-bundle-format (advertised in --help); otherwise it is a 3.x
// that does not advertise the flag.
const fakeCosign = `#!/bin/sh
echo "cosign $*" >>"$FAKE_LOG"
[ "$1" = verify-blob-attestation ] || exit 64
shift
[ "$1" = "--help" ] && { [ -n "$FAKE_COSIGN_V2" ] && echo "      --new-bundle-format   expect the bundle in Sigstore format"; exit 0; }
bundle= identity= ptype= issuer= newfmt= file=
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift ;;
    --certificate-identity-regexp) identity=$2; shift ;;
    --certificate-oidc-issuer) issuer=$2; shift ;;
    --type) ptype=$2; shift ;;
    --new-bundle-format) newfmt=1 ;;
    *) file=$1 ;;
  esac
  shift
done
if [ -n "$FAKE_COSIGN_V2" ] && [ -z "$newfmt" ]; then echo "Error: bundle is not in the legacy format"; exit 1; fi
[ "$identity" = "$FAKE_IDENTITY" ] || { echo "wrong signer identity: $identity"; exit 1; }
[ "$issuer" = https://token.actions.githubusercontent.com ] || { echo "wrong issuer: $issuer"; exit 1; }
[ "$ptype" = "$FAKE_PREDICATE" ] || { echo "wrong predicate type: $ptype"; exit 1; }
sum=$(sha256sum "$file" | awk '{print $1}')
statement=$(tr -d ' \n' <"$bundle" | grep -o '"payload":"[^"]*"' | sed 's/^"payload":"//; s/"$//' | base64 -d)
case "$statement" in *"\"sha256\":\"$sum\""*) ;; *) echo "Error: provided artifact digests do not match digests in statement"; exit 1 ;; esac
echo "Verified OK"
`

type sigEnv struct {
	path string // PATH for install.sh
	log  string // every fake verifier invocation, one per line
}

// verifierEnv builds a PATH of the toolbox plus the named fake verifiers.
func verifierEnv(t *testing.T, fakes ...string) sigEnv {
	t.Helper()
	bin := t.TempDir()
	for _, name := range fakes {
		body := map[string]string{"gh": fakeGh, "cosign": fakeCosign}[name]
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return sigEnv{path: bin + string(os.PathListSeparator) + toolbox(t), log: filepath.Join(t.TempDir(), "verifier.log")}
}

func (e sigEnv) env(extra ...string) []string {
	return append([]string{"PATH=" + e.path, "FAKE_LOG=" + e.log, "FAKE_IDENTITY=" + signerIdentity, "FAKE_PREDICATE=" + predicateType}, extra...)
}

func (e sigEnv) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// sign attaches to tag a fake Sigstore bundle shaped like actions/attest's: a
// DSSE envelope whose in-toto statement names the release's current host
// archive as its subject and tag as the release in its predicate.
func (r *release) sign(t *testing.T, tag string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	archive, ok := r.files["/download/"+tag+"/"+hostAsset()]
	if !ok {
		t.Fatalf("no %s archive to sign", tag)
	}
	r.files["/download/"+tag+"/"+signatureAsset] = fakeBundle(t, tag, sha256Hex(archive))
}

func fakeBundle(t *testing.T, tag, digest string) []byte {
	t.Helper()
	type subject struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}
	statement, err := json.Marshal(struct {
		Type          string            `json:"_type"`
		Subject       []subject         `json:"subject"`
		PredicateType string            `json:"predicateType"`
		Predicate     map[string]string `json:"predicate"`
	}{
		Type:          "https://in-toto.io/Statement/v1",
		Subject:       []subject{{Name: hostAsset(), Digest: map[string]string{"sha256": digest}}},
		PredicateType: predicateType,
		Predicate:     map[string]string{"repository": "reliant-labs/forge", "release": tag, "commit": strings.Repeat("c", 40)},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.MarshalIndent(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(statement),
			"payloadType": "application/vnd.in-toto+json",
			"signatures":  []map[string]string{{"sig": "fake"}},
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

// tamper is RF-2's attacker: someone who can write release assets swaps the
// archive AND rewrites checksums.txt to match it, so the sha256 check passes.
// The signature stays the original's: it cannot be re-made without the
// release workflow's identity.
func (r *release) tamper(t *testing.T, tag string) {
	t.Helper()
	evil := fakeArchive(t, "evil")
	sums := []byte(fmt.Sprintf("%s  %s\n", sha256Hex(evil), hostAsset()))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files["/download/"+tag+"/"+hostAsset()] = evil
	r.files["/download/"+tag+"/checksums.txt"] = sums
}

// replay is the same attacker with an older, legitimately signed release in
// hand: it copies that release's archive, checksums.txt and signature over
// tag's, so every digest matches a valid signature, made for the wrong release.
func (r *release) replay(t *testing.T, from, tag string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range []string{hostAsset(), "checksums.txt", signatureAsset} {
		body, ok := r.files["/download/"+from+"/"+f]
		if !ok {
			t.Fatalf("%s has no %s to replay", from, f)
		}
		r.files["/download/"+tag+"/"+f] = body
	}
}

func requireNotInstalled(t *testing.T, res installResult) {
	t.Helper()
	if _, err := os.Stat(res.binary); err == nil {
		t.Errorf("a binary was installed although verification failed:\n%s", res.out)
	}
}

func TestInstallSh_VerifiesTheReleaseSignatureWithGh(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	rel.sign(t, "v0.9.0")
	ve := verifierEnv(t, "gh", "cosign")

	res := runInstall(t, "sh", rel, "", ve.env()...)
	if res.err != nil {
		t.Fatalf("install failed: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "against the release signature (gh;") {
		t.Errorf("install did not report a gh-verified signature:\n%s", res.out)
	}
	if strings.Contains(res.out, "warning:") {
		t.Errorf("a verified install must not warn:\n%s", res.out)
	}
	calls := ve.calls(t)
	if !strings.Contains(calls, "gh attestation verify ") || !strings.Contains(calls, "--bundle ") || !strings.Contains(calls, "--deny-self-hosted-runners") {
		t.Errorf("gh was not asked to verify the bundle offline:\n%s", calls)
	}
	if strings.Contains(calls, "cosign ") {
		t.Errorf("cosign ran although gh can verify:\n%s", calls)
	}
}

// RF-2's exploit: the archive and checksums.txt are replaced together. The
// sha256 check passes; the signature check must refuse, with either verifier.
func TestInstallSh_TamperedReleaseWithMatchingChecksumIsRefused(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fakes []string
		extra []string
	}{
		{"gh", []string{"gh"}, nil},
		{"cosign", []string{"cosign"}, nil},
		{"cosign-2.x", []string{"cosign"}, []string{"FAKE_COSIGN_V2=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := newRelease(t)
			rel.publish(t, "v0.9.0", true, goodSum)
			rel.sign(t, "v0.9.0")
			rel.tamper(t, "v0.9.0")
			ve := verifierEnv(t, tc.fakes...)

			res := runInstall(t, "sh", rel, "", ve.env(tc.extra...)...)
			if res.err == nil {
				t.Fatalf("a tampered release installed:\n%s", res.out)
			}
			if !strings.Contains(res.out, "the release signature does not verify for "+hostAsset()) {
				t.Errorf("error should say the signature does not verify:\n%s", res.out)
			}
			if strings.Contains(res.out, "sha256 mismatch") {
				t.Errorf("the attacker's checksums.txt matches; the sha256 check must have passed:\n%s", res.out)
			}
			requireNotInstalled(t, res)
		})
	}
}

func TestInstallSh_FallsBackToCosign(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fakes []string
		extra []string
	}{
		{"no-gh", []string{"cosign"}, nil},
		{"cosign-2.x", []string{"cosign"}, []string{"FAKE_COSIGN_V2=1"}},
		// gh older than 2.49 has no `attestation` command.
		{"old-gh", []string{"gh", "cosign"}, []string{"FAKE_NO_ATTESTATION=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := newRelease(t)
			rel.publish(t, "v0.8.0", false, goodSum)
			rel.sign(t, "v0.8.0")
			ve := verifierEnv(t, tc.fakes...)

			res := runInstall(t, "sh", rel, "", ve.env(append([]string{"FORGE_VERSION=v0.8.0"}, tc.extra...)...)...)
			if res.err != nil {
				t.Fatalf("install failed: %v\n%s", res.err, res.out)
			}
			if !strings.Contains(res.out, "against the release signature (cosign;") {
				t.Errorf("install did not verify with cosign:\n%s", res.out)
			}
		})
	}
}

// A release with no signature (every release cut before signing began)
// installs with a warning, and never asks a verifier anything.
func TestInstallSh_MissingSignatureWarns(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	ve := verifierEnv(t, "gh")

	res := runInstall(t, "sh", rel, "", ve.env()...)
	if res.err != nil {
		t.Fatalf("an unsigned release must still install by default: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "warning: v0.9.0 (the latest release) carries no signature ("+signatureAsset+")") || !strings.Contains(res.out, "FORGE_REQUIRE_SIGNATURE=1") {
		t.Errorf("expected a missing-signature warning naming the opt-in:\n%s", res.out)
	}
	if strings.Contains(ve.calls(t), "verify ") {
		t.Errorf("no verifier should run without a bundle:\n%s", ve.calls(t))
	}
}

func TestInstallSh_RequireSignatureRefusesUnsignedRelease(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	ve := verifierEnv(t, "gh")

	res := runInstall(t, "sh", rel, "", ve.env("FORGE_REQUIRE_SIGNATURE=1")...)
	if res.err == nil {
		t.Fatalf("FORGE_REQUIRE_SIGNATURE=1 installed an unsigned release:\n%s", res.out)
	}
	if !strings.Contains(res.out, "carries no signature") || !strings.Contains(res.out, "FORGE_REQUIRE_SIGNATURE=1 refuses") {
		t.Errorf("error should explain the refusal:\n%s", res.out)
	}
	requireNotInstalled(t, res)
}

func TestInstallSh_NoVerifierWarnsUnlessRequired(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	rel.sign(t, "v0.9.0")
	ve := verifierEnv(t) // neither gh nor cosign

	res := runInstall(t, "sh", rel, "", ve.env()...)
	if res.err != nil {
		t.Fatalf("install without a verifier must still succeed by default: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "warning: neither gh (2.49 or later) nor cosign is installed") {
		t.Errorf("expected a no-verifier warning:\n%s", res.out)
	}

	res = runInstall(t, "sh", rel, "", ve.env("FORGE_REQUIRE_SIGNATURE=true")...)
	if res.err == nil {
		t.Fatalf("FORGE_REQUIRE_SIGNATURE installed without verifying:\n%s", res.out)
	}
	if !strings.Contains(res.out, "neither gh (2.49 or later) nor cosign is installed") {
		t.Errorf("error should name the missing verifier:\n%s", res.out)
	}
	requireNotInstalled(t, res)
}

func TestInstallSh_RejectsBadRequireSignatureValue(t *testing.T) {
	testkit.Slow(t, "execs sh")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	res := runInstall(t, "sh", rel, "", "FORGE_REQUIRE_SIGNATURE=maybe")
	if res.err == nil || !strings.Contains(res.out, "FORGE_REQUIRE_SIGNATURE must be 1 or 0") {
		t.Fatalf("an unrecognised FORGE_REQUIRE_SIGNATURE must be rejected, got err=%v\n%s", res.err, res.out)
	}
	if len(rel.requests) != 0 {
		t.Errorf("a rejected setting must not reach the network, saw %v", rel.requests)
	}
}

// A valid signature from an older release, replayed with that release's
// archive and checksums.txt under a newer tag (pinned or resolved from
// latest): every digest verifies, but the statement names the older release,
// so the install is refused, with either verifier.
func TestInstallSh_ReplayedOlderReleaseIsRefused(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fakes []string
		extra []string
	}{
		{"gh-latest", []string{"gh"}, nil},
		{"gh-pinned", []string{"gh"}, []string{"FORGE_VERSION=v0.9.0"}},
		{"cosign-pinned", []string{"cosign"}, []string{"FORGE_VERSION=v0.9.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := newRelease(t)
			rel.publish(t, "v0.8.0", false, goodSum)
			rel.sign(t, "v0.8.0")
			rel.publish(t, "v0.9.0", true, goodSum)
			rel.sign(t, "v0.9.0")
			rel.replay(t, "v0.8.0", "v0.9.0")
			ve := verifierEnv(t, tc.fakes...)

			res := runInstall(t, "sh", rel, "", ve.env(append([]string{"FORGE_REQUIRE_SIGNATURE=1"}, tc.extra...)...)...)
			if res.err == nil {
				t.Fatalf("an older release replayed under v0.9.0 installed:\n%s", res.out)
			}
			if !strings.Contains(res.out, "it was not made for reliant-labs/forge v0.9.0") {
				t.Errorf("error should say the signature is for another release:\n%s", res.out)
			}
			requireNotInstalled(t, res)
		})
	}
}

// A signed install reports the release the signature was bound to; the latest
// release is resolved to its tag and downloaded from it.
func TestInstallSh_LatestIsResolvedAndBound(t *testing.T) {
	testkit.Slow(t, "execs sh, curl and tar")
	requireUnix(t)
	t.Parallel()
	rel := newRelease(t)
	rel.publish(t, "v0.9.0", true, goodSum)
	rel.sign(t, "v0.9.0")
	ve := verifierEnv(t, "gh")

	res := runInstall(t, "sh", rel, "", ve.env("FORGE_REQUIRE_SIGNATURE=1")...)
	if res.err != nil {
		t.Fatalf("install failed: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "release v0.9.0)") {
		t.Errorf("install did not report the bound release:\n%s", res.out)
	}
	rel.mu.Lock()
	defer rel.mu.Unlock()
	for _, p := range rel.requests {
		if strings.HasPrefix(p, "/latest/download") {
			t.Errorf("install downloaded through /latest/download instead of the resolved tag: %v", rel.requests)
		}
	}
}
