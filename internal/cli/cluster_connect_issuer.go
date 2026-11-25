package cli

// `forge cluster connect` registers the cluster's service-account ISSUER with
// the control plane, so the secret store can verify the projected tokens an
// External Secrets Operator in that cluster logs in with (secrets design §2.2:
// one JWT auth mount per cluster, verified against the cluster's OWN keys; no
// stored credential, and no call from the store back into the cluster).
//
// Two ways the store learns the keys, chosen with --issuer:
//
//	discover  the store fetches <issuer>/.well-known/openid-configuration. For an
//	          issuer reachable from the internet (GKE's is).
//	upload    forge reads /openid/v1/jwks through the owner's context and sends
//	          it. For a cluster the store cannot reach (private, k3d).
//
// Upload is a SNAPSHOT: a rotated signing key is followed by re-running
// connect, which re-reads and re-sends the set (the control plane replaces the
// registration, so a retired key stops verifying).
//
// PUBLIC KEYS ONLY, and refused rather than stripped: a key set carrying a
// private or symmetric member is never sent, because dropping the secret half
// would still have read it into this process and the control plane refuses such
// a set anyway.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/statefile"
)

const (
	issuerAuto     = "auto"
	issuerDiscover = "discover"
	issuerUpload   = "upload"
	issuerSkip     = "skip"
	issuerWithdraw = "withdraw"

	oidcDiscoveryPath = "/.well-known/openid-configuration"
	oidcJWKSPath      = "/openid/v1/jwks"
)

// parseIssuerMode validates --issuer.
func parseIssuerMode(spec string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(spec)); s {
	case "", issuerAuto:
		return issuerAuto, nil
	case issuerDiscover, issuerUpload, issuerSkip, issuerWithdraw:
		return s, nil
	default:
		return "", fmt.Errorf("--issuer %q is not auto, discover, upload, skip or withdraw\n"+
			"auto registers the cluster's issuer (discover on GKE, upload elsewhere) and carries on without it when it cannot; "+
			"skip leaves the registration alone; withdraw removes it", spec)
	}
}

// connectIssuerPlan is what this run asks of the control plane's issuer
// registry, resolved BEFORE anything is written.
type connectIssuerPlan struct {
	// Requested is the mode the owner asked for; Mode is what it resolved to
	// (discover, upload, skip or withdraw).
	Requested, Mode string
	Issuer          string
	// JWKS is the normalized public key set (upload only).
	JWKS        string
	KeyCount    int
	Fingerprint string
	// Note says why an auto run registered nothing.
	Note string
}

func (p connectIssuerPlan) registers() bool {
	return p.Mode == issuerDiscover || p.Mode == issuerUpload
}

// wire is the ConnectClusterRequest.issuer member. Presence is the switch on
// the control plane: absent leaves a registration alone, present and empty
// withdraws.
func (p connectIssuerPlan) wire() (map[string]any, bool) {
	switch p.Mode {
	case issuerDiscover:
		return map[string]any{"issuer": p.Issuer, "discover": true}, true
	case issuerUpload:
		return map[string]any{"issuer": p.Issuer, "jwks": p.JWKS}, true
	case issuerWithdraw:
		return map[string]any{}, true
	}
	return nil, false
}

// connectAPIGet reads one non-resource path from the API server of a kubectl
// context. A var so tests serve the paths from a fake or a real test apiserver.
var connectAPIGet = kubeAPIGet

// kubeAPIGet is connectAPIGet's production reader, through the client-go
// config of the operator's kubeconfig.
func kubeAPIGet(ctx context.Context, kctx, path string) ([]byte, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kctx}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load context %q: %w", kctx, err)
	}
	cfg.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return cs.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(ctx)
}

// publishedIssuer is what a cluster publishes about its tokens.
type publishedIssuer struct {
	Issuer      string
	JWKS        string
	Keys        int
	Fingerprint string
}

// readClusterIssuer reads the issuer (and, for upload, the key set) the
// cluster publishes.
func readClusterIssuer(ctx context.Context, kctx string, withKeys bool) (pub publishedIssuer, err error) {
	hint := func(path string, err error) error {
		return fmt.Errorf("read %s through context %q: %w\n"+
			"the API server must publish its service-account issuer, and the context's user must be allowed `get` on %s "+
			"(a cluster admin is, and so is any authenticated ServiceAccount through system:service-account-issuer-discovery)", path, kctx, err, path)
	}
	raw, err := connectAPIGet(ctx, kctx, oidcDiscoveryPath)
	if err != nil {
		return publishedIssuer{}, hint(oidcDiscoveryPath, err)
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || strings.TrimSpace(doc.Issuer) == "" {
		return publishedIssuer{}, fmt.Errorf("%s through context %q names no issuer", oidcDiscoveryPath, kctx)
	}
	pub.Issuer = strings.TrimSpace(doc.Issuer)
	if !withKeys {
		return pub, nil
	}
	raw, err = connectAPIGet(ctx, kctx, oidcJWKSPath)
	if err != nil {
		return publishedIssuer{}, hint(oidcJWKSPath, err)
	}
	pub.JWKS, pub.Keys, pub.Fingerprint, err = normalizeJWKS(raw)
	if err != nil {
		return publishedIssuer{}, fmt.Errorf("%s through context %q: %w", oidcJWKSPath, kctx, err)
	}
	return pub, nil
}

// privateJWKMembers are the members of a JSON Web Key that only a private or
// symmetric key carries (RFC 7517/7518: RSA d p q dp dq qi oth, EC/OKP d, oct k).
var privateJWKMembers = []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"}

// normalizeJWKS validates a key set and returns it in the form that is sent:
// compact, keys in a stable order, so the same set is the same bytes. It refuses
// a set holding anything but public keys.
func normalizeJWKS(raw []byte) (jwks string, keys int, fingerprint string, err error) {
	var set struct {
		Keys []map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return "", 0, "", errors.New("is not a JSON Web Key Set")
	}
	if len(set.Keys) == 0 {
		return "", 0, "", errors.New("holds no keys")
	}
	type entry struct {
		sum  string
		body []byte
	}
	entries := make([]entry, 0, len(set.Keys))
	for i, k := range set.Keys {
		if _, ok := k["kty"]; !ok {
			return "", 0, "", fmt.Errorf("key %d has no kty", i)
		}
		for _, m := range privateJWKMembers {
			if _, ok := k[m]; ok {
				return "", 0, "", fmt.Errorf("key %d carries the private or symmetric member %q: forge sends public keys only and will not send this set", i, m)
			}
		}
		body, err := json.Marshal(k)
		if err != nil {
			return "", 0, "", err
		}
		sum := sha256.Sum256(body)
		entries = append(entries, entry{sum: hex.EncodeToString(sum[:]), body: body})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sum < entries[j].sum })
	ordered := make([]json.RawMessage, len(entries))
	h := sha256.New()
	for i, e := range entries {
		ordered[i] = e.body
		h.Write([]byte(e.sum))
	}
	out, err := json.Marshal(map[string]any{"keys": ordered})
	if err != nil {
		return "", 0, "", err
	}
	return string(out), len(entries), hex.EncodeToString(h.Sum(nil))[:16], nil
}

// planConnectIssuer resolves --issuer against what the cluster publishes.
//
// auto NEVER FAILS A CONNECT over the issuer: a cluster that publishes nothing
// readable, or a control plane that refuses the declaration (see
// issuerRefusal), still connects for deploys; the plan records why nothing was
// registered and the run says so. An explicit discover or upload is strict.
func planConnectIssuer(ctx context.Context, mode, kctx string, gke, offline bool) (connectIssuerPlan, error) {
	p := connectIssuerPlan{Requested: mode, Mode: mode}
	switch mode {
	case issuerSkip, issuerWithdraw:
		return p, nil
	case issuerAuto:
		p.Mode = issuerUpload
		if gke {
			p.Mode = issuerDiscover
		}
	}
	if offline {
		p.Note = "not read: --live-from reviews offline"
		return p, nil
	}
	pub, err := readClusterIssuer(ctx, kctx, p.Mode == issuerUpload)
	if err != nil {
		if mode == issuerAuto {
			return connectIssuerPlan{Requested: mode, Mode: issuerSkip, Note: "the cluster's issuer could not be read: " + err.Error()}, nil
		}
		return p, err
	}
	p.Issuer, p.JWKS, p.KeyCount, p.Fingerprint = pub.Issuer, pub.JWKS, pub.Keys, pub.Fingerprint
	return p, nil
}

// issuerHint explains a declined issuer in terms of the cluster, when the
// cause is a known one. k3s (so k3d) signs tokens with
// https://kubernetes.default.svc.<cluster domain>, and the control plane refuses a
// `.local` host: it can never be a public name, and a discovery issuer is
// dialed by the store. An uploaded key set never needs the issuer to resolve,
// only to equal the tokens' iss, so a name that is not `.local` is enough.
func issuerHint(issuer string) string {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(issuer, "https://"), "/"))
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	if !strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal") && host != "localhost" {
		return ""
	}
	return fmt.Sprintf("\n  the cluster's issuer is %s. On k3s/k3d, start the API server with\n"+
		"    --kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc\n"+
		"  (k3d: --k3s-arg \"--kube-apiserver-arg=service-account-issuer=https://kubernetes.default.svc@server:*\"), "+
		"then re-run connect", issuer)
}

// issuerRefusal reports whether err is the control plane declining the issuer
// declaration or the store behind it. The control plane checks the declaration
// BEFORE it connects anything, so a refusal leaves the cluster unregistered and
// connect can be repeated without it.
func issuerRefusal(err error) bool {
	var ce *cloud.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.HasCode(cloud.CodeInvalidArgument) || ce.HasCode(cloud.CodeAlreadyExists) ||
		ce.HasCode(cloud.CodeUnavailable) || ce.HasCode(cloud.CodeFailedPrecondition)
}

// ── the response, and what is kept of it ────────────────────────────────────

// wireClusterIssuer is the subset of controlplane.v1.ClusterIssuer forge reads.
type wireClusterIssuer struct {
	StoreClusterID string `json:"storeClusterId,omitempty"`
	Issuer         string `json:"issuer,omitempty"`
	Discover       bool   `json:"discover,omitempty"`
	KeyCount       int32  `json:"keyCount,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
}

// connectIssuerRecord is what connect keeps of a registration:
// .forge/state/cluster-issuer-<cluster>.json. The store cluster id is the one
// value later commands must not re-derive, because it is the control plane's to
// assign (it is globally unique, first org wins). Nothing in it is secret.
type connectIssuerRecord struct {
	Cluster        string `json:"cluster"`
	Env            string `json:"env"`
	StoreClusterID string `json:"store_cluster_id"`
	Issuer         string `json:"issuer"`
	Mode           string `json:"mode"`
	KeyCount       int    `json:"key_count"`
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// connectStateDir is the project directory the record lives under. A var so
// tests keep it out of the working tree.
var connectStateDir = projectDirForKCL

func connectIssuerStatePath(cluster string) string {
	return statefile.Path(connectStateDir(), "cluster-issuer-"+statefile.SafeSegment(cluster)+".json")
}

func readConnectIssuerRecord(cluster string) *connectIssuerRecord {
	rec, err := statefile.Read[connectIssuerRecord](connectIssuerStatePath(cluster), "cluster issuer")
	if err != nil {
		return nil
	}
	return rec
}

func removeConnectIssuerRecord(cluster string) error {
	return os.Remove(connectIssuerStatePath(cluster))
}

// reportConnectIssuer records what the control plane now holds and says it.
// None of it fails the connect: the cluster is registered either way.
func reportConnectIssuer(out io.Writer, name, env string, plan connectIssuerPlan, got *wireClusterIssuer) {
	statePath := connectIssuerStatePath(name)
	switch {
	case plan.Mode == issuerWithdraw:
		_ = removeConnectIssuerRecord(name)
		fmt.Fprintf(out, "  issuer    withdrawn: the secret store no longer trusts this cluster's tokens\n")
		return
	case !plan.registers():
		if plan.Note != "" {
			fmt.Fprintf(out, "\nWARNING: this cluster's issuer was NOT registered with the secret store: %s\n"+
				"  Deploys are unaffected. Store-backed Secrets (forge.StoreSource) on this cluster cannot log in until it is:\n"+
				"  fix it, then re-run `forge cluster connect` (--issuer upload or discover makes a failure an error).\n", plan.Note)
		}
		return
	case got == nil || got.StoreClusterID == "":
		fmt.Fprintf(out, "\nWARNING: the control plane reported no registered issuer for %s. It predates issuer registration, or has no\n"+
			"  secret store; the cluster is connected for deploys only.\n", name)
		return
	}

	prev := readConnectIssuerRecord(name)
	rec := connectIssuerRecord{
		Cluster: name, Env: env, StoreClusterID: got.StoreClusterID, Issuer: got.Issuer, Mode: plan.Mode,
		KeyCount: int(got.KeyCount), KeyFingerprint: plan.Fingerprint, UpdatedAt: got.UpdatedAt,
	}
	if rec.Issuer == "" {
		rec.Issuer = plan.Issuer
	}
	how := "the store fetches the keys from the issuer"
	if plan.Mode == issuerUpload {
		how = fmt.Sprintf("%d key(s) uploaded", plan.KeyCount)
		switch {
		case prev == nil || prev.Mode != issuerUpload:
		case prev.KeyFingerprint == plan.Fingerprint:
			how += ", unchanged since the last connect"
		default:
			how += fmt.Sprintf(", REPLACED the previous set of %d (key rotation followed)", prev.KeyCount)
		}
	}
	fmt.Fprintf(out, "  issuer    %s\n  keys      %s\n", rec.Issuer, how)
	fmt.Fprintf(out, "  store id  %s   (auth mount jwt-%s)\n", rec.StoreClusterID, rec.StoreClusterID)
	if rec.StoreClusterID != name {
		fmt.Fprintf(out, "\nWARNING: the store knows this cluster as %q, not %q. A SecretStore renders its mount and audience from the\n"+
			"  connected cluster name, so name the cluster %q in the env's KCL (ClusterTarget.connected_cluster, or\n"+
			"  Bundle.connected_clusters) or the store login will not find its mount.\n", rec.StoreClusterID, name, rec.StoreClusterID)
	}
	if err := statefile.Write(statePath, "cluster issuer", rec); err != nil {
		fmt.Fprintf(out, "note: could not keep the issuer registration under .forge/state: %v\n", err)
	}
}
