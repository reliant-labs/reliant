// Package hostedimage holds the ONE vocabulary for "can the control plane pull
// this hosted image": the push-base resolution rule, the off-base finding, and
// its wording.
//
// It exists as its own package because the rule has three consumers that
// cannot import each other:
//
//   - internal/cli's build, which resolves a bare hosted image to the push
//     base and refuses one it cannot resolve;
//   - internal/cli's `env render`, which refuses a host-bearing image it can
//     prove is off-base;
//   - internal/cli/lint, which reports the same thing as a finding and must
//     not import internal/cli.
//
// Without a shared package the third consumer would restate the message, and a
// restated message is one that drifts — which matters here because the message
// IS the product: an author who misreads it declares the wrong registry and
// discovers it at publish time, which is the defect ADR-0003 F1 closes.
//
// Everything here is pure and offline. No consumer of this package may make a
// call: a render that needed a credential would stop being the reproducible
// projection every other surface diffs against.
package hostedimage

import (
	"fmt"
	"regexp"
	"strings"
)

// NormalizeBase trims the one difference that is not a difference: a trailing
// slash. Every join below is explicit.
func NormalizeBase(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/")
}

// HasRegistryHost reports whether a reference's first path component is a
// registry host — the same rule kcl/lib/images.k and v1alpha1.ValidateImage
// apply, so forge's three layers agree about what "bare" means.
func HasRegistryHost(reference string) bool {
	first, _, ok := strings.Cut(reference, "/")
	return ok && (strings.ContainsAny(first, ".:") || first == "localhost")
}

// Repository strips any `:tag` / `@digest`, keeping the registry host.
func Repository(image string) string {
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	lastSlash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > lastSlash {
		return image[:colon]
	}
	return image
}

// Name is a repository's last path segment — the artifact name
// (`ghcr.io/acme/shop` → `shop`).
func Name(repository string) string {
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		return repository[slash+1:]
	}
	return repository
}

// ResolveBase is the resolution rule (ADR-0003 F1): a BARE image on a hosted
// item becomes `<base>/<image>`; a host-bearing one is returned verbatim.
//
// base "" returns the image unchanged. That is deliberately not an error here
// — this function has no env to name and no opinion about whether the caller
// can proceed. The caller decides: a build refuses (it has nowhere to push),
// a render warns (it has nothing to compare).
func ResolveBase(base, image string) string {
	if image == "" || HasRegistryHost(image) {
		return image
	}
	base = NormalizeBase(base)
	if base == "" {
		return image
	}
	return base + "/" + image
}

// UnderBase reports whether a reference sits inside the subtree base names.
// An image already under it is correct, merely redundant — the author wrote
// the platform's own registry out in full.
func UnderBase(base, image string) bool {
	base = NormalizeBase(base)
	if base == "" {
		return false
	}
	repo := Repository(image)
	return repo == base || strings.HasPrefix(repo, base+"/")
}

// Item is one hosted thing that carries an image: a workload or a frontend.
// Owner is what the message names, because a project may declare several and
// only one of them is wrong.
type Item struct {
	Owner string
	Image string
	// Unbuilt marks a workload forge does NOT build (no `build`): its image
	// is someone else's — a third-party image or one CI pushed. A host-bearing
	// unbuilt image outside the push base is not a finding: forge copies it
	// into the push base at release time (MirrorRepository) and the release
	// pins the copy by digest. A BUILT image outside the base is: forge would
	// push its own build somewhere the platform does not pull from.
	Unbuilt bool
}

// MirrorRepository is where forge copies a THIRD-PARTY image so the platform
// can pull it: `<base>/<MirrorSourcePath(image)>` — the repository minus its
// registry host, so `docker.io/traefik/whoami:v1.10.3` becomes
// `<base>/traefik/whoami`. The path is kept whole (not just its last segment)
// so two sources that share a name (`bitnami/redis`, `library/redis`) cannot
// land in one repository.
//
// "" when there is nothing to mirror: no base, a bare image (it already
// resolves under the base, ResolveBase), or an image already under the base.
// The result is not yet known to be a name the registry accepts:
// ValidateMirror says.
func MirrorRepository(base, image string) string {
	base = NormalizeBase(base)
	repo := Repository(image)
	if base == "" || !HasRegistryHost(repo) || UnderBase(base, repo) {
		return ""
	}
	return base + "/" + MirrorSourcePath(repo)
}

// dockerHubHosts are the spellings of Docker Hub's registry. A reference on
// any of them names one repository namespace, and a one-segment path there is
// an official image under the implicit `library/` namespace.
var dockerHubHosts = map[string]bool{
	"docker.io":            true,
	"index.docker.io":      true,
	"registry-1.docker.io": true,
}

// MirrorSourcePath is a host-bearing repository's path with the registry host
// removed and Docker Hub's implicit namespace written out, so every spelling
// of one upstream repository maps to one path: `docker.io/nginx`,
// `index.docker.io/nginx` and `docker.io/library/nginx` are all
// `library/nginx`. Other registries keep their path verbatim.
func MirrorSourcePath(repository string) string {
	host, path, _ := strings.Cut(Repository(repository), "/")
	if dockerHubHosts[host] && !strings.Contains(path, "/") {
		return "library/" + path
	}
	return path
}

// CanonicalSource is a source reference's repository with Docker Hub's host
// spellings and implicit namespace normalized (`docker.io/library/nginx`), so
// two spellings of one upstream repository compare equal.
func CanonicalSource(repository string) string {
	host, _, _ := strings.Cut(Repository(repository), "/")
	if dockerHubHosts[host] {
		host = "docker.io"
	}
	return host + "/" + MirrorSourcePath(repository)
}

// mirrorComponentRe is the distribution spec's repository path component:
// lowercase alphanumerics, separated by `.`, `_`, `__` or runs of `-`.
var mirrorComponentRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*$`)

// maxMirrorReferenceLen is the longest image name (host and path) the
// reference grammar the registry and the container runtime parse accepts.
const maxMirrorReferenceLen = 255

// ValidateMirror reports why a mirror destination is not a repository the
// platform registry would accept: a path component outside the distribution
// grammar, or a name past the reference length limit. Checked before anything
// is copied, so a release cut is refused with the cause rather than failing
// half-way through a copy.
func ValidateMirror(destination string) error {
	if len(destination) > maxMirrorReferenceLen {
		return fmt.Errorf("mirror repository %s is %d characters, over the %d a registry accepts", destination, len(destination), maxMirrorReferenceLen)
	}
	_, path, _ := strings.Cut(destination, "/")
	for _, c := range strings.Split(path, "/") {
		if !mirrorComponentRe.MatchString(c) {
			return fmt.Errorf("mirror repository %s has path component %q, which is not a valid repository name "+
				"(lowercase letters and digits, separated by '.', '_', '__' or '-')", destination, c)
		}
	}
	return nil
}

// Finding is one host-bearing image on a hosted item.
type Finding struct {
	Owner string
	Image string
	// Base is the subtree the platform admits, or "" when forge could not
	// learn it. This is the field that decides how much the finding claims.
	Base string
}

// Verified reports whether forge actually COMPARED this image against a known
// base, as opposed to merely noticing that a host was declared.
//
// The distinction is load-bearing, not cosmetic. A verified finding is a fact
// — forge holds the admitted subtree and this image is not in it, so the
// publish will be refused. An unverified one is an observation: a hosted
// author does not need to declare a host, and forge could not check the one
// they declared. Acting on the second as though it were the first means
// failing a render over an assertion forge never made, and on a project whose
// registry IS the platform's that assertion would be false.
func (f Finding) Verified() bool { return f.Base != "" }

// Message is the one sentence a reader acts on. The unverified wording claims
// strictly less, for the reason Verified documents.
func (f Finding) Message() string {
	bare := Name(Repository(f.Image))
	if f.Verified() {
		return fmt.Sprintf("%s: image %s is outside this org's image push base %s, and the control plane admits only its own registry. "+
			"Drop the registry host (image = %q) and forge resolves it to %s/%s",
			f.Owner, f.Image, f.Base, bare, f.Base, bare)
	}
	return fmt.Sprintf("%s: host-bearing image %s on an OnHosted workload: the control plane admits only its own registry; "+
		"drop the host and forge resolves it", f.Owner, f.Image)
}

// FixHint is the remediation, separated for the structured reporters.
func (f Finding) FixHint() string {
	return fmt.Sprintf("set image = %q on %s; forge composes the platform's registry onto it",
		Name(Repository(f.Image)), f.Owner)
}

// OffBase is every host-bearing image among items, judged against base when
// base is known.
//
// An UNBUILT item's image is never a finding either: forge mirrors it into the
// base at release time (MirrorRepository), so it is pullable whatever registry
// it names.
//
// A BARE image is never a finding: that is the resolved, correct shape. The
// caller that must refuse an unresolvable bare image does so separately
// (internal/cli: checkHostedImagesResolve), because the remedy differs —
// there the declaration cannot be shipped at all, here it can be shipped
// somewhere the platform will not pull from.
func OffBase(items []Item, base string) []Finding {
	base = NormalizeBase(base)
	var out []Finding
	for _, it := range items {
		if it.Image == "" || it.Unbuilt || !HasRegistryHost(Repository(it.Image)) {
			continue
		}
		if UnderBase(base, it.Image) {
			continue
		}
		out = append(out, Finding{Owner: it.Owner, Image: it.Image, Base: base})
	}
	return out
}

// Verified narrows findings to the ones forge proved.
func Verified(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Verified() {
			out = append(out, f)
		}
	}
	return out
}
