package cli

// THE CODE SOURCE: which repository, which directory of it and which commit a
// release or a deploy came from, and the Reliant project that ran it. Sent to
// the control plane with every release cut (`forge env build --release`, a
// composed release), every `forge release record` and every deploy attempt,
// so an app can name its code: the app → code half of the code↔app link
// (control-plane docs/design/unified-shell-and-projects.md, §8.1 and D3; the
// control plane stores it in hosted_project_sources).
//
// THE WIRE SHAPE is controlplane.v1.DeployCodeSource, the optional field
// `code_source` on CutReleaseRequest, on OpenDeployAttemptRequest and on
// ReportDeployAttemptRequest (the last for a deploy whose release is known
// only after the attempt opened: forAttempt):
//
//	message DeployCodeSource {
//	  string remote = 1;             // "host/owner/repo"; "" when there is no shareable remote
//	  string subpath = 2;            // forge.yaml's directory in the repo, slash-separated; "" at the root
//	  string commit = 3;             // full object id; "" when unknown (see forRelease, forAttempt)
//	  string reliant_project_id = 4; // RELIANT_PROJECT_ID, when a Reliant chat ran forge
//	}
//
// SENT AHEAD OF THE SERVER. A control plane that predates the field drops it
// (Connect's JSON codec discards unknown fields) and records everything else
// as before; nothing is negotiated and nothing waits on it.
//
// IT NEVER FAILS ANYTHING. A directory that is not a git checkout, a
// repository with no commits, no origin, a remote that is a local path: each
// sends what is known and leaves the rest empty. A dirty tree sends its HEAD,
// the commit the change was made on; the release's provenance already says
// it was dirty.
//
// NOTHING LEAVES THAT NAMES THIS MACHINE. The remote must first be one of
// git's network forms (a network-scheme URL or [user@]host:path), is then
// canonicalized (release.CanonicalRepo: no scheme, no credentials, no
// ".git"), and is dropped unless it reads as a hosted repository, so neither
// a token in a URL nor a local path, absolute or relative, reaches the ledger.
//
// ONE SOURCE IS ONE TUPLE. Remote, subpath and commit always describe the
// same code: a deploy of a release cut elsewhere sends that release's
// repository and commit, never the deploying checkout's remote beside the
// release's commit (forRelease).

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// reliantProjectIDEnv names the Reliant project a chat runs in. The Reliant
// daemon sets it next to RELIANT_FORGE_ORG in a chat's environment.
const reliantProjectIDEnv = "RELIANT_PROJECT_ID"

// codeSourceTimeout bounds the git reads. A var so tests can shrink it.
var codeSourceTimeout = 5 * time.Second

// codeSource is one DeployCodeSource. The zero value is "nothing known".
type codeSource struct {
	Remote           string
	Subpath          string
	Commit           string
	ReliantProjectID string
}

// wire is the proto3 JSON of the source, or nil when it says nothing — so a
// request from outside any checkout and any chat is the request it was.
func (s codeSource) wire() map[string]any {
	out := map[string]any{}
	putNonEmptyAny(out, "remote", s.Remote)
	putNonEmptyAny(out, "subpath", s.Subpath)
	putNonEmptyAny(out, "commit", s.Commit)
	putNonEmptyAny(out, "reliantProjectId", s.ReliantProjectID)
	if len(out) == 0 {
		return nil
	}
	return out
}

func putNonEmptyAny(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// forRelease is the source of the release r — what a cut or record of r
// sends, and what a deploy of an already-cut r sends: r's own repository and
// commit, as r recorded them. It is the tuple of the release, not of the
// checkout that happens to run the command, which may be another clone,
// another branch, or another repository altogether.
//
// The checkout adds its directory and its chat ONLY when r's provenance names
// a shareable repository and it is the checkout's own: then it is the same
// app's code, and forge.yaml's directory in it is r's too. A release whose
// provenance is absent, names no repository, or names one that is not
// shareable (a local path) sends its commit alone, and nothing when it names
// no commit either: pairing the checkout's remote, directory or chat with a
// release nothing says this checkout built would be false evidence — a
// `release record` in CI, or a release cut in another repository.
//
// NEVER THE CHECKOUT'S HEAD. A release names the commit it was built or
// recorded from; a release that names none, or names an invalid one, has an
// unknown commit, and sending the HEAD of whatever checkout ran the cut
// would record false provenance — `release record` in CI runs from a
// checkout that need not be at the recorded commit.
func (s codeSource) forRelease(r release.Release) codeSource {
	out := codeSource{Remote: releaseRemote(r), Commit: releaseCommit(r)}
	if out.Remote != "" && out.Remote == s.Remote {
		out.Subpath, out.ReliantProjectID = s.Subpath, s.ReliantProjectID
	}
	return out
}

// forAttempt is the source a deploy attempt sends, and whether it is known
// yet. The checkout's own source (HEAD included) only when the deploy ships
// the checkout — no release named, no --from env — because then the release
// it cuts is built from that HEAD. A deploy of an already-cut release sends
// that release's tuple (forRelease). shipped is nil while that release is
// unknown — a --from deploy resolves it after the attempt opens, or the read
// failed — and then nothing is known yet: the attempt sends no source rather
// than a checkout's repository beside a release it did not build, and sends
// the shipped one on a later report (deploy_attempt.go).
func (s codeSource) forAttempt(shipsCheckout bool, shipped *release.Release) (codeSource, bool) {
	if shipsCheckout {
		return s, true
	}
	if shipped == nil {
		return codeSource{}, false
	}
	return s.forRelease(*shipped), true
}

// releaseCommit is the commit r names: its provenance's, else its git
// record's; "" when it names none or an invalid one.
func releaseCommit(r release.Release) string {
	commit := r.Git.Commit
	if r.Provenance != nil && r.Provenance.Commit != "" {
		commit = r.Provenance.Commit
	}
	if commit = strings.ToLower(strings.TrimSpace(commit)); release.ValidObjectID(commit) {
		return commit
	}
	return ""
}

// releaseRemote is the shareable repository r's provenance recorded, "" when
// it recorded none. Provenance holds an ALREADY canonical remote, so it is
// screened as one (shareableCanonical): the original URL that would say
// whether it was a network remote is gone.
func releaseRemote(r release.Release) string {
	if r.Provenance == nil {
		return ""
	}
	return shareableCanonical(release.CanonicalRepo(r.Provenance.Repo))
}

// codeSourceProbe captures the code source of one checkout, once per process,
// on first use: a command that never cuts or deploys runs no git for it.
type codeSourceProbe struct {
	dir     string
	getenv  func(string) string
	capture func(ctx context.Context, dir string, getenv func(string) string) codeSource

	once sync.Once
	src  codeSource
}

func newCodeSourceProbe(dir string, getenv func(string) string) *codeSourceProbe {
	return &codeSourceProbe{dir: dir, getenv: getenv, capture: captureCodeSource}
}

// get is the captured source. A nil probe — a ledger opened with no checkout
// behind it — knows nothing.
func (p *codeSourceProbe) get(ctx context.Context) codeSource {
	if p == nil {
		return codeSource{}
	}
	p.once.Do(func() { p.src = p.capture(ctx, p.dir, p.getenv) })
	return p.src
}

// attachCodeSource makes a hosted ledger send the code source of the checkout
// at projectDir with what it records. Any other ledger is returned unchanged:
// a file ledger is on this machine and needs no link back to it.
func attachCodeSource(l envLedger, projectDir string, getenv func(string) string) envLedger {
	if hs, ok := l.Bindings.(*hostedStore); ok && hs.source == nil && projectDir != "" {
		hs.source = newCodeSourceProbe(projectDir, getenv)
	}
	return l
}

// captureCodeSource reads dir's code source. Every git failure is an empty
// field, never an error: see the file comment.
func captureCodeSource(ctx context.Context, dir string, getenv func(string) string) codeSource {
	var s codeSource
	if getenv != nil {
		s.ReliantProjectID = reliantProjectID(getenv(reliantProjectIDEnv))
	}
	ctx, cancel := context.WithTimeout(ctx, codeSourceTimeout)
	defer cancel()
	// --show-prefix is dir's path below the top of its checkout, with a
	// trailing slash, "" at the top, and fails outside a checkout — which
	// ends the capture, since nothing else git could say would apply.
	prefix, err := codeSourceGit(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		return s
	}
	s.Subpath = strings.Trim(prefix, "/")
	var wg sync.WaitGroup
	wg.Go(func() {
		if c, err := codeSourceGit(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil && release.ValidObjectID(c) {
			s.Commit = c
		}
	})
	wg.Go(func() {
		if url, err := codeSourceGit(ctx, dir, "remote", "get-url", "origin"); err == nil {
			s.Remote = shareableRemote(url)
		}
	})
	wg.Wait()
	return s
}

func codeSourceGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// shareableHost is a repository host as a remote names one: a DNS name or an
// address. A single letter (a Windows drive) is not one.
var shareableHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)

// networkSchemes are the URL schemes git reaches another machine with.
// file:// is not one, and neither is a remote helper (<transport>::<address>).
var networkSchemes = map[string]bool{
	"https": true, "http": true, "ssh": true, "git": true,
	"git+ssh": true, "ssh+git": true, "ftp": true, "ftps": true,
}

// shareableRemote is a git remote URL as the "host/owner/repo" it names on a
// network, or "" when it does not name a hosted repository: a local path,
// relative or absolute, a file:// URL, a remote helper, anything still
// carrying a credential, query or fragment after canonicalization.
//
// CLASSIFIED BEFORE IT IS NORMALIZED. release.CanonicalRepo strips a scheme,
// so a file:// URL or a relative path such as "sibling/repo" comes out of it
// shaped like "host/owner/repo". The ORIGINAL url must first read as one of
// git's network forms (networkRemote), and only then is it canonicalized.
func shareableRemote(url string) string {
	network, ok := networkRemote(strings.TrimSpace(url))
	if !ok {
		return ""
	}
	return shareableCanonical(release.CanonicalRepo(network))
}

// networkRemote classifies u as git does (`git help urls`) and returns it as
// a URL CanonicalRepo reads correctly: a URL with a network scheme and a
// host, as it is; the SCP form [user@]host:path — a colon before any slash —
// as ssh://[user@]host/path, because CanonicalRepo reads a user-less
// "host:owner/repo" as a host with a port. Anything else is local.
func networkRemote(u string) (string, bool) {
	if u == "" || strings.ContainsAny(u, " \t\r\n\\") {
		return "", false
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		if !networkSchemes[strings.ToLower(scheme)] {
			return "", false
		}
		authority, _, _ := strings.Cut(rest, "/")
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			authority = authority[at+1:]
		}
		return u, authority != ""
	}
	if strings.Contains(u, "::") {
		return "", false
	}
	colon, slash := strings.Index(u, ":"), strings.Index(u, "/")
	if colon <= 0 || (slash >= 0 && slash < colon) {
		return "", false
	}
	userHost, path := u[:colon], u[colon+1:]
	user, host := "", userHost
	if at := strings.LastIndex(userHost, "@"); at >= 0 {
		user, host = userHost[:at+1], userHost[at+1:]
	}
	if host == "" || strings.Trim(path, "/") == "" {
		return "", false
	}
	return "ssh://" + user + host + "/" + path, true
}

// shareableCanonical is canon, an already canonical "host/owner/repo", when it
// names a repository another machine can reach, else "". The host must be
// fully qualified — a DNS name with a dot, or an address — and not this
// machine: a bare name such as "gitserver" resolves only through this
// machine's ssh config or search domain, and it is also what a relative path
// ("sibling/repo") looks like once a scheme is gone.
func shareableCanonical(canon string) string {
	if canon == "" || strings.ContainsAny(canon, "@?#\\ \t\r\n") {
		return ""
	}
	host, path, ok := strings.Cut(canon, "/")
	if !ok || !shareableHost.MatchString(host) || strings.Trim(path, "/") == "" {
		return ""
	}
	if !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasPrefix(host, "127.") {
		return ""
	}
	return canon
}

// reliantProjectIDPattern bounds RELIANT_PROJECT_ID to an opaque id: the
// variable comes from the environment, and a malformed one is dropped rather
// than sent.
var reliantProjectIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func reliantProjectID(v string) string {
	v = strings.TrimSpace(v)
	if !reliantProjectIDPattern.MatchString(v) {
		return ""
	}
	return v
}
