package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// covCFlowOnlyBundle declares no ingress at all: one host workload that serves a
// declared flow check on its listen port, so smoke has only the app-flow half to run.
func covCFlowOnlyBundle(port int) string {
	return `{"output":{"workloads":[{
	  "name":"api","kind":"service",
	  "flow_checks":[{"path":"/flow","name":"api-flow","description":"flow is healthy"}],
	  "runtime":{"type":"host","runner":"go-run","listen_ports":[` + strconv.Itoa(port) + `]},
	  "spec":{"kind":"service"}}]}}`
}

func covCServerPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := splitHostPortForTest(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func splitHostPortForTest(rawURL string) (string, int, error) {
	i := strings.LastIndex(rawURL, ":")
	p, err := strconv.Atoi(rawURL[i+1:])
	return rawURL[:i], p, err
}

func TestCovC_RunSmoke_FlowOnlyEnv(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flow" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer srv.Close()
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCFlowOnlyBundle(covCServerPort(t, srv))))
	ctx := context.Background()

	t.Run("healthy flow check is reported without any route table", func(t *testing.T) {
		var buf bytes.Buffer
		err := runSmokeWith(ctx, "dev", smokeOptions{timeout: 5 * time.Second, flowProbe: probeFlowHealth}, noGateway, noRouteProbe, &buf)
		if err != nil {
			t.Fatalf("err = %v\n%s", err, buf.String())
		}
		out := buf.String()
		for _, want := range []string{"forge env smoke dev", "checking declared app-flow endpoints only", "api-flow", "summary: 1 PASS, 0 WARN, 0 FAIL  (1 check(s))", "every app-flow check is healthy"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("an unhealthy flow check fails the run and the error counts it", func(t *testing.T) {
		status = http.StatusServiceUnavailable
		defer func() { status = http.StatusOK }()
		var buf bytes.Buffer
		err := runSmokeWith(ctx, "dev", smokeOptions{timeout: 5 * time.Second, flowProbe: probeFlowHealth}, noGateway, noRouteProbe, &buf)
		if err == nil || !strings.Contains(err.Error(), "smoke dev: 1 app-flow check(s) FAILED") {
			t.Fatalf("err = %v\n%s", err, buf.String())
		}
		if !strings.Contains(buf.String(), "FAILED — ingress and/or app-flow is not healthy") {
			t.Errorf("verdict missing:\n%s", buf.String())
		}
	})
	t.Run("json output carries the flow result and the failure", func(t *testing.T) {
		status = http.StatusServiceUnavailable
		defer func() { status = http.StatusOK }()
		var buf bytes.Buffer
		err := runSmokeWith(ctx, "dev", smokeOptions{jsonOut: true, tag: "v1", timeout: 5 * time.Second, flowProbe: probeFlowHealth}, noGateway, noRouteProbe, &buf)
		if err == nil {
			t.Fatal("want a failing exit")
		}
		var rep smokeJSONReport
		if jerr := json.Unmarshal(buf.Bytes(), &rep); jerr != nil {
			t.Fatalf("json: %v\n%s", jerr, buf.String())
		}
		if rep.Summary.OK || rep.Summary.Fail != 1 || len(rep.Routes) != 1 {
			t.Fatalf("report = %+v", rep)
		}
	})
	t.Run("runSmoke defaults the timeout and the real flow probe", func(t *testing.T) {
		out := captureStdout(t, func() {
			if err := runSmoke(ctx, "dev", smokeOptions{}); err != nil {
				t.Errorf("runSmoke: %v", err)
			}
		})
		if !strings.Contains(out, "api-flow") {
			t.Errorf("out = %q", out)
		}
	})
}

func TestCovC_RunSmokeWith_RenderFailureNamesTheFix(t *testing.T) {
	prev := renderKCLRawHook
	t.Cleanup(func() { renderKCLRawHook = prev })
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("kcl: syntax error")
	}
	var buf bytes.Buffer
	err := runSmokeWith(context.Background(), "staging", smokeOptions{}, noGateway, noRouteProbe, &buf)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"smoke staging: render KCL", "kcl: syntax error", "deploy/kcl/staging/", "forge env deploy staging --dry-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestCovC_GatewayNoAddrDetail(t *testing.T) {
	if got := gatewayNoAddrDetail("public", nil); got != `gateway "public" has no external address (not programmed / LB pending)` {
		t.Errorf("no error: %q", got)
	}
	if got := gatewayNoAddrDetail("public", errors.New("forbidden")); got != `gateway "public": forbidden` {
		t.Errorf("with error: %q", got)
	}
}

func TestCovC_ResolveGatewayIPsDeduplicatesAndKeepsErrors(t *testing.T) {
	e := &KCLEntities{
		HTTPRoutes: []HTTPRouteEntity{{Gateway: "public"}, {Gateway: "public"}, {Gateway: "internal"}, {Gateway: ""}},
		GRPCRoutes: []GRPCRouteEntity{{Gateway: "internal"}, {Gateway: "broken"}},
	}
	asked := map[string]int{}
	boom := errors.New("not found")
	ips, errs := resolveGatewayIPs(context.Background(), e, "ctx", "ns", func(_ context.Context, kctx, ns, gw string) (string, error) {
		asked[gw]++
		if kctx != "ctx" || ns != "ns" {
			t.Errorf("resolver got context %q namespace %q", kctx, ns)
		}
		if gw == "broken" {
			return "", boom
		}
		return "10.0.0." + strconv.Itoa(len(gw)), nil
	})
	if asked["public"] != 1 || asked["internal"] != 1 || asked["broken"] != 1 || len(asked) != 3 {
		t.Errorf("each distinct gateway is resolved exactly once: %v", asked)
	}
	if ips["public"] != "10.0.0.6" || ips["internal"] != "10.0.0.8" || len(ips) != 2 {
		t.Errorf("ips = %v", ips)
	}
	if !errors.Is(errs["broken"], boom) || len(errs) != 1 {
		t.Errorf("errs = %v", errs)
	}
}

func TestCovC_ResolveGatewayIP(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"kubectl": `case "$*" in *missing*) echo "not found" >&2; exit 1;; esac; echo " 203.0.113.7 "`})
	ctx := context.Background()
	got, err := resolveGatewayIP(ctx, "gke_prod", "web", "public")
	if err != nil || got != "203.0.113.7" {
		t.Fatalf("= %q, %v", got, err)
	}
	if calls := covCCalls(t, logPath); calls[0] != "kubectl --context gke_prod -n web get gateway public -o jsonpath={.status.addresses[0].value}" {
		t.Errorf("kubectl argv = %q", calls[0])
	}
	if _, err := resolveGatewayIP(ctx, "", "", "x"); err != nil {
		t.Fatal(err)
	}
	if calls := covCCalls(t, logPath); strings.Contains(calls[1], "--context") || strings.Contains(calls[1], " -n ") {
		t.Errorf("empty context and namespace must not be passed: %q", calls[1])
	}
	_, err = resolveGatewayIP(ctx, "gke_prod", "", "missing")
	if err == nil || !strings.Contains(err.Error(), `kubectl get gateway missing`) || !strings.Contains(err.Error(), `is the context "gke_prod" reachable`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCovC_ProbeRouteClassifiesTransportFailures(t *testing.T) {
	target := smokeTarget{Host: "api.example.com", Path: "/healthz"}
	t.Run("a cancelled context is a transport failure, not a pass", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := probeRoute(ctx, target, "127.0.0.1", time.Second)
		if res.Status != smokeStatusFail || res.Reason != smokeReasonTLS {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("a host that cannot form a URL fails without dialing", func(t *testing.T) {
		res := probeRoute(context.Background(), smokeTarget{Host: "bad host", Path: "/x"}, "127.0.0.1", time.Second)
		if res.Status != smokeStatusFail {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestCovC_SmokeHTTPClientContract(t *testing.T) {
	c := smokeHTTPClient("api.example.com", "203.0.113.7", 3*time.Second)
	if c.Timeout != 3*time.Second {
		t.Errorf("timeout = %s", c.Timeout)
	}
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("a redirect is a result, not something to follow: %v", err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig.ServerName != "api.example.com" || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("SNI must be the route host with verification on: %+v", tr)
	}
}
