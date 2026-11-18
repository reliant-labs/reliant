package templates

import (
	"go/format"
	"regexp"
	"strings"
	"testing"
)

// cfgFieldRE matches a Cfg field declared or set in code (not prose).
var cfgFieldRE = regexp.MustCompile(`(?m)^\t+Cfg[: ]`)

// TestAdapterTemplates_TakeTheConfigBlock pins #748. The adapter scaffold
// declared `BaseURL string` on Deps: forge's own forge-config-deps lint
// flagged every freshly scaffolded adapter, and compose.go could only wire
// the scalar to `""` with a TODO. When the scaffold composes the adapter's
// config block on AppConfig, the adapter takes that block (`Cfg
// *config.<Name>Config`) — the shape the lint asks for and the one compose.go
// resolves by type — and its test points base_url at the stub through it.
func TestAdapterTemplates_TakeTheConfigBlock(t *testing.T) {
	data := observePkgData{
		Name: "stripe", ImportPath: "stripe", Module: "example.com/proj", Flavor: "adapter",
		ConfigBlock: "StripeConfig", ConfigEnvVar: "STRIPE_BASE_URL",
	}
	render := func(name string) string {
		t.Helper()
		out, err := InternalPkgKindTemplates("adapter").Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		if _, ferr := format.Source(out); ferr != nil {
			t.Fatalf("rendered %s is not gofmt-valid: %v\n%s", name, ferr, out)
		}
		return string(out)
	}

	adapter := render("adapter.go.tmpl")
	for _, want := range []string{
		`"example.com/proj/pkg/config"`,
		"Cfg *config.StripeConfig",
		"s.deps.Cfg.GetBaseUrl()",
		"STRIPE_BASE_URL",
	} {
		if !strings.Contains(adapter, want) {
			t.Errorf("adapter.go missing %q:\n%s", want, adapter)
		}
	}
	if strings.Contains(adapter, "BaseURL string") || strings.Contains(adapter, "deps.BaseURL") {
		t.Errorf("adapter.go still declares or reads the scalar BaseURL Deps field:\n%s", adapter)
	}

	test := render("adapter_test.go.tmpl")
	if !strings.Contains(test, "&config.StripeConfig{BaseUrl: stub.URL}") {
		t.Errorf("adapter_test.go must point the config block's base_url at the stub:\n%s", test)
	}
	if strings.Contains(test, "BaseURL:") {
		t.Errorf("adapter_test.go still sets the scalar BaseURL Deps field:\n%s", test)
	}
}

// An adapter scaffolded with --base-url uses its config block, with no legacy
// scalar fallback.
func TestAdapterTemplates_NoScalarFallback(t *testing.T) {
	data := observePkgData{Name: "stripe", ImportPath: "stripe", Module: "example.com/proj", Flavor: "adapter", ConfigBlock: "StripeConfig", ConfigEnvVar: "STRIPE_BASE_URL"}
	for _, name := range []string{"adapter.go.tmpl", "adapter_test.go.tmpl"} {
		out, err := InternalPkgKindTemplates("adapter").Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		if _, ferr := format.Source(out); ferr != nil {
			t.Fatalf("rendered %s is not gofmt-valid: %v\n%s", name, ferr, out)
		}
		if !cfgFieldRE.MatchString(string(out)) {
			t.Errorf("%s must use the config block:\n%s", name, out)
		}
		if strings.Contains(string(out), "BaseURL string") || strings.Contains(string(out), "BaseURL:") {
			t.Errorf("%s retains the scalar fallback:\n%s", name, out)
		}
	}
}

// TestAdapterTemplates_WithoutABaseURL pins the default shape: an adapter's
// downstream is not assumed to be one HTTP API, so with no config block the
// templates import no pkg/config, declare no Cfg, read no base URL, and
// HealthCheck fails until a probe is written instead of reporting healthy
// while probing nothing.
func TestAdapterTemplates_WithoutABaseURL(t *testing.T) {
	data := observePkgData{Name: "pubsubpull", ImportPath: "pubsubpull", Module: "example.com/proj", Flavor: "adapter"}
	for _, name := range []string{"adapter.go.tmpl", "adapter_test.go.tmpl", "contract.go.tmpl"} {
		out, err := InternalPkgKindTemplates("adapter").Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		formatted, ferr := format.Source(out)
		if ferr != nil {
			t.Fatalf("rendered %s is not gofmt-valid: %v\n%s", name, ferr, out)
		}
		if string(formatted) != string(out) {
			t.Errorf("rendered %s is not gofmt-clean:\n%s", name, out)
		}
		unwanted := []string{`/pkg/config"`, "BASE_URL", "GetBaseUrl"}
		if name == "adapter_test.go.tmpl" {
			unwanted = append(unwanted, "httptest") // a stub needs a base URL to point at
		}
		for _, unwanted := range unwanted {
			if strings.Contains(string(out), unwanted) {
				t.Errorf("%s without a config block contains %q:\n%s", name, unwanted, out)
			}
		}
		if cfgFieldRE.MatchString(string(out)) {
			t.Errorf("%s without a config block declares or sets Cfg:\n%s", name, out)
		}
	}
	adapter, err := InternalPkgKindTemplates("adapter").Render("adapter.go.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adapter), "errNoHealthProbe") || !strings.Contains(string(adapter), "HTTPClient *http.Client") {
		t.Errorf("adapter.go without a config block must keep the traced client and fail HealthCheck until it probes:\n%s", adapter)
	}
}
