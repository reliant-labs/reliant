package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const v2ProjectGoMod = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"

// A project born on connect-go v2 gets the v2 auth-policy pair: middleware.go
// declaring NewAuthInterceptorV2 (a connect.ServerInterceptor of
// connectrpc.com/connect/v2) and the policy test that drives it. A v1
// project's pair is exactly what it always was.
func TestGeneratePkgMiddleware_FollowsTheConnectMajor(t *testing.T) {
	for _, tt := range []struct {
		major     connectmajor.Major
		want, not []string
		testWant  string
		testNot   string
	}{
		{
			major:    connectmajor.V1,
			want:     []string{"\t\"connectrpc.com/connect\"\n", "func NewAuthInterceptor(deps AuthDeps) (connect.Interceptor, error) {", "authn.NewInterceptor(authn.Policy{"},
			not:      []string{"connect/v2", "NewAuthInterceptorV2"},
			testWant: "ic.WrapUnary(",
			testNot:  "NewAuthInterceptorV2",
		},
		{
			major:    connectmajor.V2,
			want:     []string{"\tconnect \"connectrpc.com/connect/v2\"\n", "func NewAuthInterceptorV2(deps AuthDeps) (connect.ServerInterceptor, error) {", "authn.NewInterceptorV2(authn.Policy{"},
			not:      []string{"\t\"connectrpc.com/connect\"\n", "func NewAuthInterceptor("},
			testWant: "NewAuthInterceptorV2(AuthDeps{Validate: authenticator.Authenticate})",
			testNot:  "WrapUnary",
		},
	} {
		t.Run(tt.major.String(), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "pkg", "middleware"), 0o755); err != nil {
				t.Fatal(err)
			}
			g := NewProjectGenerator("app", dir, "example.com/app")
			g.ConnectMajor = tt.major
			if err := g.generatePkgMiddleware(); err != nil {
				t.Fatal(err)
			}
			src := readFile(t, filepath.Join(dir, "pkg", "middleware", "middleware.go"))
			for _, w := range tt.want {
				if !strings.Contains(src, w) {
					t.Errorf("middleware.go lacks %q", w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(src, n) {
					t.Errorf("middleware.go has %q", n)
				}
			}
			test := readFile(t, filepath.Join(dir, "pkg", "middleware", "middleware_test.go"))
			if !strings.Contains(test, tt.testWant) || strings.Contains(test, tt.testNot) {
				t.Errorf("middleware_test.go is not the %s policy test:\n%s", tt.major, test)
			}
			if strings.HasPrefix(test, "//go:build ignore") {
				t.Error("middleware_test.go kept the template's //go:build ignore")
			}
		})
	}
}

// `forge project upgrade` re-derives the untemplated middleware_test.go; a v2
// project's copy is the v2 policy test, not the v1 one its middleware.go
// cannot satisfy.
func TestRenderManagedFile_RawConnectV2Twin(t *testing.T) {
	f := managedFile{templateName: "middleware_test.go"}
	v1, err := renderManagedFile(f, projectTemplateData{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := renderManagedFile(f, projectTemplateData{ConnectV2: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v1), "NewAuthInterceptor(AuthDeps{") || strings.Contains(string(v1), "NewAuthInterceptorV2") {
		t.Error("v1 upgrade render is not the v1 policy test")
	}
	if !strings.Contains(string(v2), "NewAuthInterceptorV2(AuthDeps{") || strings.Contains(string(v2), "connect.NewRequest") {
		t.Error("v2 upgrade render is not the v2 policy test")
	}
	// And the policy file it tests, which is templated.
	mw := managedFile{templateName: "middleware.go.tmpl", templated: true}
	v1, err = renderManagedFile(mw, projectTemplateData{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err = renderManagedFile(mw, projectTemplateData{Name: "app", ConnectV2: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v1), "func NewAuthInterceptor(deps AuthDeps)") || !strings.Contains(string(v2), "func NewAuthInterceptorV2(deps AuthDeps)") {
		t.Error("the upgrade lane does not render middleware.go at the project's connect-go major")
	}
}

// The e2e harness is scaffold-once and born at the project's major: a v2
// project's client wraps a connecthttp transport (unbounded reads, as v1) and
// its rows are bare messages driven by tdd.TableRPCV2.
func TestGenerateE2ETests_FollowsTheConnectMajor(t *testing.T) {
	methods := []E2EMethodInfo{{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"}}
	for _, v2 := range []bool{false, true} {
		dir := t.TempDir()
		if v2 {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(v2ProjectGoMod), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := GenerateE2ETests(dir, "orders", "example.com/app", "app", methods); err != nil {
			t.Fatal(err)
		}
		helpers := readFile(t, filepath.Join(dir, "e2e", "orders", "helpers_test.go"))
		svc := readFile(t, filepath.Join(dir, "e2e", "orders", "service_test.go"))
		if v2 {
			for _, w := range []string{"connect.NewClient(", "connecthttp.NewTransport(http.DefaultClient, baseURL, connecthttp.WithReadMaxBytes(0))"} {
				if !strings.Contains(helpers, w) {
					t.Errorf("v2 helpers_test.go lacks %q:\n%s", w, helpers)
				}
			}
			for _, w := range []string{"tdd.TableRPCV2(", "Req:     &pb.GetOrderRequest{},", "connect \"connectrpc.com/connect/v2\""} {
				if !strings.Contains(svc, w) {
					t.Errorf("v2 service_test.go lacks %q:\n%s", w, svc)
				}
			}
			if strings.Contains(svc, "connect.NewRequest") {
				t.Error("v2 service_test.go wraps a request in connect.NewRequest")
			}
		} else if !strings.Contains(svc, "connect.NewRequest(&pb.GetOrderRequest{})") || strings.Contains(helpers, "connecthttp") {
			t.Errorf("v1 e2e harness changed:\n%s\n%s", helpers, svc)
		}
	}
}

// `forge project new --connect v2 --service orders`, end to end through the
// scaffold: go.mod and gen/go.mod require connect-go v2, and the service,
// policy and test harness born with it are v2 — the service files are
// scaffolded after go.mod, whose direct require is what they read the major
// from.
func TestProjectGeneratorGenerate_ConnectV2(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := filepath.Join(t.TempDir(), "shop")
	g := NewProjectGenerator("shop", root, "example.com/shop")
	g.ServiceName = "orders"
	g.ConnectMajor = connectmajor.V2
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got := connectmajor.DetectOrDefault(root); got != connectmajor.V2 {
		t.Fatalf("the scaffolded go.mod selects %s, want v2", got)
	}
	for path, want := range map[string]string{
		"go.mod":                                       "\tconnectrpc.com/connect/v2 v2.0.2\n",
		"gen/go.mod":                                   "\tconnectrpc.com/connect/v2 v2.0.2\n",
		"tools/tools.go":                               "_ \"connectrpc.com/connect/v2/cmd/protoc-gen-connect-go\"",
		"internal/handlers/orders/service.go":          "func (s *Service) RegisterV2(server *connect.Server) {",
		"pkg/middleware/middleware.go":                 "func NewAuthInterceptorV2(deps AuthDeps) (connect.ServerInterceptor, error) {",
		"pkg/middleware/middleware_test.go":            "NewAuthInterceptorV2(AuthDeps{",
		"internal/handlers/orders/helpers_gen_test.go": "testkit.NewConnectClientV2(t, svc.RegisterV2, ordersv1connect.NewOrdersServiceClient, interceptors...)",
	} {
		if got := readFile(t, filepath.Join(root, path)); !strings.Contains(got, want) {
			t.Errorf("%s lacks %q:\n%s", path, want, got)
		}
	}
	if got := readFile(t, filepath.Join(root, "go.mod")); strings.Contains(got, "connectrpc.com/connect v1") {
		t.Errorf("go.mod requires connect-go v1:\n%s", got)
	}
}
