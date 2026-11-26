// Package sigstore is the keyless Sigstore [selfupdate.Verifier]: it checks a
// release's files against the attestation bundle its release workflow made
// with actions/attest, in process, with no cosign or gh on the machine.
//
// A release verifies only when all of these hold (docs/proposals/self-upgrade.md
// §3, the scheme forge's install.sh and reliant's dev-docs/cli-release-artifacts.md
// already use):
//
//   - the bundle chains to the Sigstore trusted root (Fulcio certificate,
//     transparency-log entry, certificate timestamps);
//   - the certificate's identity matches [Config.Identity] (the release
//     workflow at a release ref) and its issuer [Config.Issuer];
//   - the in-toto statement's predicate type is [Config.PredicateType];
//   - EVERY downloaded file but the bundle is a subject of the statement,
//     under its own name and with its own sha256;
//   - the predicate names [Config.Repo] as its repository and the tag being
//     installed as its release, so an older signed set replayed under a newer
//     tag is refused.
package sigstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

// GitHubActionsIssuer is the OIDC issuer of a GitHub Actions workflow's
// certificate, the default [Config.Issuer].
const GitHubActionsIssuer = "https://token.actions.githubusercontent.com"

// maxBundleBytes bounds the bundle read into memory; a real one is ~11 KiB.
const maxBundleBytes = 1 << 20

// Config is who may sign a release and what the signature must claim.
type Config struct {
	// Repo is "owner/name". The signed predicate's repository must equal it.
	Repo string
	// Identity is a regular expression the certificate's subject alternative
	// name (the signing workflow's URI at its ref) must match. It must be
	// anchored (^…$): an unanchored pattern admits any workflow that merely
	// contains it.
	Identity string
	// Issuer is the certificate's OIDC issuer; default [GitHubActionsIssuer].
	Issuer string
	// PredicateType is the in-toto predicate type the release workflow signs.
	PredicateType string

	// TrustedRoot is a Sigstore trusted_root.json. Nil fetches the public-good
	// instance's root through TUF at each verification, which follows key
	// rotations; a pinned root does not, so set one only for tests or an
	// air-gapped machine that refreshes it some other way.
	TrustedRoot []byte
	// TUFCachePath caches the TUF metadata when TrustedRoot is nil. Empty
	// keeps it in memory only.
	TUFCachePath string
}

// Verifier is a [selfupdate.Verifier].
type Verifier struct {
	cfg      Config
	identity verify.CertificateIdentity
	pinned   root.TrustedMaterial
}

var _ selfupdate.Verifier = (*Verifier)(nil)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// New validates cfg.
func New(cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		cfg.Issuer = GitHubActionsIssuer
	}
	switch {
	case !repoRE.MatchString(cfg.Repo):
		return nil, fmt.Errorf("sigstore: Config.Repo must be owner/name, got %q", cfg.Repo)
	case cfg.PredicateType == "":
		return nil, errors.New("sigstore: Config.PredicateType is required")
	case !strings.HasPrefix(cfg.Identity, "^") || !strings.HasSuffix(cfg.Identity, "$"):
		return nil, fmt.Errorf("sigstore: Config.Identity must be an anchored regular expression (^…$), got %q", cfg.Identity)
	}
	if _, err := regexp.Compile(cfg.Identity); err != nil {
		return nil, fmt.Errorf("sigstore: Config.Identity: %w", err)
	}
	identity, err := verify.NewShortCertificateIdentity(cfg.Issuer, "", "", cfg.Identity)
	if err != nil {
		return nil, fmt.Errorf("sigstore: certificate identity: %w", err)
	}
	v := &Verifier{cfg: cfg, identity: identity}
	if cfg.TrustedRoot != nil {
		tr, err := root.NewTrustedRootFromJSON(cfg.TrustedRoot)
		if err != nil {
			return nil, fmt.Errorf("sigstore: Config.TrustedRoot: %w", err)
		}
		v.pinned = tr
	}
	return v, nil
}

// trustedMaterial is the pinned root, or the public-good root fetched now.
func (v *Verifier) trustedMaterial(ctx context.Context) (root.TrustedMaterial, error) {
	if v.pinned != nil {
		return v.pinned, nil
	}
	opts := tuf.DefaultOptions().WithContext(ctx)
	if v.cfg.TUFCachePath == "" {
		opts.DisableLocalCache = true
	} else {
		opts = opts.WithCachePath(v.cfg.TUFCachePath)
	}
	tr, err := root.FetchTrustedRootWithOptions(opts)
	if err != nil {
		return nil, fmt.Errorf("sigstore: fetch the Sigstore trusted root: %w", err)
	}
	return tr, nil
}

// Verify checks files (asset name to local path) against rel.Bundle. Every
// file but the bundle itself must be a signed subject.
func (v *Verifier) Verify(ctx context.Context, rel selfupdate.Release, files map[string]string) error {
	if rel.Tag == "" {
		return errors.New("sigstore: the release has no tag to bind the signature to")
	}
	if rel.Bundle.Name == "" {
		return fmt.Errorf("sigstore: release %s carries no signature bundle; unsigned releases are refused", rel.Tag)
	}
	bundlePath, ok := files[rel.Bundle.Name]
	if !ok {
		return fmt.Errorf("sigstore: the signature bundle %s was not downloaded", rel.Bundle.Name)
	}
	raw, err := readBounded(bundlePath, maxBundleBytes)
	if err != nil {
		return fmt.Errorf("sigstore: read %s: %w", rel.Bundle.Name, err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("sigstore: parse %s: %w", rel.Bundle.Name, err)
	}

	names := make([]string, 0, len(files))
	for name := range files {
		if name != rel.Bundle.Name {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return errors.New("sigstore: nothing to verify")
	}
	sort.Strings(names)
	digests := make(map[string][]byte, len(names))
	for _, name := range names {
		d, err := sha256Of(files[name])
		if err != nil {
			return fmt.Errorf("sigstore: hash %s: %w", name, err)
		}
		digests[name] = d
	}

	tm, err := v.trustedMaterial(ctx)
	if err != nil {
		return err
	}
	sev, err := verify.NewVerifier(tm,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("sigstore: verifier: %w", err)
	}
	// The cryptographic check, the identity and EVERY downloaded file's
	// digest, in one sigstore-go verification: the DSSE signature covers the
	// whole in-toto statement, and sigstore-go refuses unless each digest is
	// one of that signed statement's subjects. The names are bound below,
	// which sigstore-go does not check.
	want := make([]verify.ArtifactDigest, 0, len(names))
	for _, name := range names {
		want = append(want, verify.ArtifactDigest{Algorithm: "sha256", Digest: digests[name]})
	}
	res, err := sev.Verify(&b, verify.NewPolicy(
		verify.WithArtifactDigests(want),
		verify.WithCertificateIdentity(v.identity),
	))
	if err != nil {
		return fmt.Errorf("sigstore: %s does not verify %s: %w", rel.Bundle.Name, strings.Join(names, ", "), err)
	}
	st := res.Statement
	if st == nil {
		return fmt.Errorf("sigstore: %s carries no in-toto statement", rel.Bundle.Name)
	}
	if st.GetPredicateType() != v.cfg.PredicateType {
		return fmt.Errorf("sigstore: predicate type is %q, want %q", st.GetPredicateType(), v.cfg.PredicateType)
	}
	// Each file must be signed under its own name with its own digest: a
	// file renamed to another signed file's name, or carrying another
	// signed file's bytes under its name, is refused.
	signed := map[string]string{}
	for _, s := range st.GetSubject() {
		name, d := s.GetName(), strings.ToLower(s.GetDigest()["sha256"])
		if name == "" || d == "" {
			continue
		}
		if prev, dup := signed[name]; dup && prev != d {
			return fmt.Errorf("sigstore: the statement signs two different files named %s", name)
		}
		signed[name] = d
	}
	for _, name := range names {
		want, ok := signed[name]
		if !ok {
			return fmt.Errorf("sigstore: %s is not one of the files %s signed", name, rel.Tag)
		}
		if want != hex.EncodeToString(digests[name]) {
			return fmt.Errorf("sigstore: %s is not the file %s signed under that name", name, rel.Tag)
		}
	}
	fields := st.GetPredicate().GetFields()
	if got := fields["repository"].GetStringValue(); got != v.cfg.Repo {
		return fmt.Errorf("sigstore: the signature is for repository %q, want %q", got, v.cfg.Repo)
	}
	if got := fields["release"].GetStringValue(); got != rel.Tag {
		return fmt.Errorf("sigstore: the signature is for release %q, not %q (a replayed signature)", got, rel.Tag)
	}
	return nil
}

func sha256Of(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("larger than %d bytes", limit)
	}
	return buf.Bytes(), nil
}
