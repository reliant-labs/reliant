//go:build !windows

// envtest's process package does not compile for windows (controller-runtime
// v0.25.2 declares signalProcess twice there); the same tag as
// internal/cluster/eso_vap_envtest_test.go.

package cli

// P15 (secrets design): the k3d issuer's JWKS upload and refresh, proven
// WITHOUT k3d. k3d is a k3s API server in a container; what matters to the
// store is that a REAL kube-apiserver serves /.well-known/openid-configuration
// and /openid/v1/jwks, and signs the tokens a pod is projected. envtest starts
// that same kube-apiserver binary, so this drives the real runClusterConnect
// through a real kubeconfig against it and checks, with real signatures:
//
//   - the grant forge renders for the control plane's identity lets exactly
//     that identity `get /openid/v1/jwks`, and without it the same request is
//     Forbidden (a non-resource URL rule, enforced by a real API server);
//   - the issuer and key set that connect UPLOADS verify a token the API server
//     itself signed (TokenRequest), and the token's `iss` is the issuer sent.
//
// The integration lane (cluster_connect_issuer_openbao_test.go) goes one step
// further and logs that token in to a real OpenBao.
//
// Needs the envtest binaries (KUBEBUILDER_ASSETS): skipped without them and
// under -short.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// envtestBindingUser is the principal forge's grant is bound to: the GCP
// service account the fake control plane reports for the binding. It belongs to
// no group, so nothing but forge's own grant can authorize it.
const envtestBindingUser = "fc-prod-ab12@acme.iam.gserviceaccount.com"

type envtestCluster struct {
	cfg     *rest.Config
	admin   *kubernetes.Clientset
	binding *kubernetes.Clientset
	caller  *fakeConnectCaller
	kctx    string
	issuer  string // the issuer connect registered
	jwks    string // the key set connect uploaded
}

func startIssuerEnvtest(t *testing.T) (*rest.Config, client.Client, *envtest.Environment) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts kube-apiserver and etcd; runs in task test")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS not set (`setup-envtest use`); the unit tests pin the request and the grant")
	}
	env := &envtest.Environment{BinaryAssetsDirectory: assets}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, admin, env
}

// applyManifestsAsAdmin is connectApply for a real API server: server-side
// apply of each document, under the field manager forge uses.
func applyManifestsAsAdmin(c client.Client) func(context.Context, string, string) error {
	return func(ctx context.Context, _ string, manifest string) error {
		for _, doc := range strings.Split(manifest, "\n---\n") {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
				return err
			}
			raw, err := json.Marshal(obj.Object)
			if err != nil {
				return err
			}
			if err := c.Patch(ctx, obj, client.RawPatch(types.ApplyPatchType, raw), client.ForceOwnership, client.FieldOwner("forge")); err != nil {
				return fmt.Errorf("apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
		return nil
	}
}

func getPath(cs *kubernetes.Clientset, path string) ([]byte, error) {
	return cs.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(context.Background())
}

// connectToEnvtest runs the real `forge cluster connect --issuer upload`
// against a real kube-apiserver, as a GKE-named context (so the binding is the
// control plane's GCP identity) read through a real kubeconfig.
func connectToEnvtest(t *testing.T) *envtestCluster {
	t.Helper()
	cfg, admin, env := startIssuerEnvtest(t)
	ctx := context.Background()
	ec := &envtestCluster{cfg: cfg, kctx: testCtx}

	adminCS, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ec.admin = adminCS
	bu, err := env.AddUser(envtest.User{Name: envtestBindingUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ec.binding, err = kubernetes.NewForConfig(bu.Config()); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"acme-prod"} {
		if _, err := adminCS.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}

	// A real kubeconfig, which connect reads through the operator's context.
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["admin"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}
	kc.Contexts[ec.kctx] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "admin"}
	kc.CurrentContext = ec.kctx
	kcPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, kcPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kcPath)

	// Before connect: the binding cannot read the key set. This is what the
	// grant's rule has to change, so a pass below is not vacuous.
	if _, err := getPath(ec.binding, oidcJWKSPath); !apierrors.IsForbidden(err) {
		t.Fatalf("before connect, the binding's GET %s = %v, want Forbidden", oidcJWKSPath, err)
	}

	s := stubConnect(t, ec.kctx, scopeStream())
	connectAPIGet = kubeAPIGet
	dir := t.TempDir()
	connectStateDir = func() string { return dir }
	connectApply = applyManifestsAsAdmin(admin)
	ec.caller = s.caller

	var out strings.Builder
	if err := runClusterConnect(ctx, clusterConnectOptions{
		Name: "prod-us", KubeContext: ec.kctx, Auth: "gcp", Env: "prod", Issuer: issuerUpload, Out: &out,
	}); err != nil {
		t.Fatalf("connect against a real API server: %v\n%s", err, out.String())
	}
	t.Logf("%s", out.String())
	reg, _ := s.caller.requests[procConnectCluster]["issuer"].(map[string]any)
	ec.issuer, _ = reg["issuer"].(string)
	ec.jwks, _ = reg["jwks"].(string)
	if ec.issuer == "" || ec.jwks == "" {
		t.Fatalf("connect sent no issuer or key set: %v", reg)
	}
	return ec
}

// uploadedKeyFunc resolves a token's key from the uploaded set only, the way
// the store's mount does (jwt_validation_pubkeys): nothing is fetched. RSA is
// what k3s and most distributions sign with; the envtest API server signs with
// ES256, so both are read.
func uploadedKeyFunc(t *testing.T, jwks string) jwt.Keyfunc {
	t.Helper()
	var set struct {
		Keys []struct {
			Kty, Kid, N, E, Crv, X, Y string
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(jwks), &set); err != nil {
		t.Fatal(err)
	}
	dec := func(s string) *big.Int {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return new(big.Int).SetBytes(b)
	}
	return func(tok *jwt.Token) (any, error) {
		for _, k := range set.Keys {
			if k.Kid != tok.Header["kid"] {
				continue
			}
			switch k.Kty {
			case "RSA":
				return &rsa.PublicKey{N: dec(k.N), E: int(dec(k.E).Int64())}, nil
			case "EC":
				if k.Crv != "P-256" {
					return nil, fmt.Errorf("curve %s", k.Crv)
				}
				point := append([]byte{4}, dec(k.X).FillBytes(make([]byte, 32))...)
				return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(point, dec(k.Y).FillBytes(make([]byte, 32))...))
			}
		}
		return nil, fmt.Errorf("no uploaded key has kid %v", tok.Header["kid"])
	}
}

func mintAPIServerToken(t *testing.T, ec *envtestCluster, aud string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := ec.admin.CoreV1().ServiceAccounts("default").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "reliant-secrets"}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	exp := int64(600)
	tr, err := ec.admin.CoreV1().ServiceAccounts("default").CreateToken(ctx, "reliant-secrets",
		&authv1.TokenRequest{Spec: authv1.TokenRequestSpec{Audiences: []string{aud}, ExpirationSeconds: &exp}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("TokenRequest: %v", err)
	}
	return tr.Status.Token
}

func TestEnvtestConnect_GrantAndUploadAgainstARealAPIServer(t *testing.T) {
	ec := connectToEnvtest(t)

	// 1. The grant forge applied is what lets the binding's identity read the key
	//    set, through a real API server.
	raw, err := getPath(ec.binding, oidcJWKSPath)
	if err != nil {
		t.Fatalf("after connect, the binding's GET %s: %v", oidcJWKSPath, err)
	}
	// It still cannot read anything the grant does not name.
	if _, err := getPath(ec.binding, "/api/v1/namespaces/kube-system/secrets"); !apierrors.IsForbidden(err) {
		t.Errorf("the binding read kube-system secrets: %v", err)
	}

	// 2. What was uploaded is the API server's own public key set, nothing else,
	//    and no private member.
	var served, sent struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(raw, &served); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(ec.jwks), &sent); err != nil {
		t.Fatal(err)
	}
	if len(served.Keys) == 0 || len(sent.Keys) != len(served.Keys) {
		t.Fatalf("uploaded %d key(s), the API server serves %d", len(sent.Keys), len(served.Keys))
	}
	for i, k := range sent.Keys {
		for _, m := range privateJWKMembers {
			if _, ok := k[m]; ok {
				t.Errorf("key %d carries %q", i, m)
			}
		}
	}

	// 3. The issuer and keys verify a token the API server really signed: the
	//    registration is enough for the store to log a pod in, with no call back
	//    into the cluster.
	const aud = "https://secrets.example.com/c/prod-us"
	token := mintAPIServerToken(t, ec, aud)
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, uploadedKeyFunc(t, ec.jwks),
		jwt.WithIssuer(ec.issuer), jwt.WithAudience(aud), jwt.WithValidMethods([]string{"RS256", "ES256"}), jwt.WithExpirationRequired()); err != nil {
		t.Fatalf("a token the API server signed does not verify against what connect registered (issuer %s): %v", ec.issuer, err)
	}
	if sub, _ := claims["sub"].(string); sub != "system:serviceaccount:default:reliant-secrets" {
		t.Errorf("sub = %q", sub)
	}
	// A token from any other key must not.
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": ec.issuer, "aud": aud, "exp": time.Now().Add(time.Minute).Unix()})
	bad, _ := forged.SignedString([]byte("not the cluster's key"))
	if _, err := jwt.Parse(bad, uploadedKeyFunc(t, ec.jwks), jwt.WithValidMethods([]string{"RS256", "ES256"})); err == nil {
		t.Error("a token signed by another key verified")
	}

	// 4. Re-running connect re-reads and re-sends the SAME bytes while the keys
	//    are unchanged (what makes a refresh a no-op, and a rotation a diff).
	var out strings.Builder
	if err := runClusterConnect(context.Background(), clusterConnectOptions{
		Name: "prod-us", KubeContext: ec.kctx, Auth: "gcp", Env: "prod", Issuer: issuerUpload, Out: &out,
	}); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out.String())
	}
	if again, _ := ec.caller.requests[procConnectCluster]["issuer"].(map[string]any); again["jwks"] != ec.jwks {
		t.Error("a re-run sent a different key set though the keys did not change")
	}
	if !strings.Contains(out.String(), "unchanged since the last connect") {
		t.Errorf("the re-run did not report the set as unchanged:\n%s", out.String())
	}
}
