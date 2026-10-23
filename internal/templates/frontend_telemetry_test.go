package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The browser-telemetry scaffold contract: forge injects and initialises the
// HyperDX browser SDK in Next.js and Vite frontends, with a same-origin
// /_otel route, no secret in the bundle, and replay off.

func renderTelemetryTmpl(t *testing.T, rel string, data FrontendTemplateData) string {
	t.Helper()
	out, err := FrontendTemplates().Render(filepath.FromSlash(rel), data)
	if err != nil {
		t.Fatalf("render %s: %v", rel, err)
	}
	return string(out)
}

func telemetryData(output string) FrontendTemplateData {
	return FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "shop",
		Output:       output,
		APIURL:       "http://localhost:8080",
	}
}

// TestTelemetryConfigIsSameOriginAndSecretFree pins, for both web scaffolds,
// that the dev default is a path on the page's own origin, that replay is
// opt-in, and that nothing resembling a credential is written into the file
// that ends up in the browser bundle.
func TestTelemetryConfigIsSameOriginAndSecretFree(t *testing.T) {
	for name, tc := range map[string]struct {
		tmpl, devDefault, replay string
	}{
		"nextjs": {
			tmpl:       "nextjs/src/lib/otel_gen.ts.tmpl",
			devDefault: "`${BASE_PATH}/_otel`",
			replay:     `process.env.NEXT_PUBLIC_OTEL_REPLAY === "true"`,
		},
		"vite-spa": {
			tmpl:       "vite-spa/src/lib/otel_gen.ts.tmpl",
			devDefault: `"/_otel"`,
			replay:     `import.meta.env.VITE_OTEL_REPLAY === "true"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := renderTelemetryTmpl(t, tc.tmpl, telemetryData("static"))

			if !strings.Contains(s, tc.devDefault) {
				t.Errorf("dev default endpoint is not the same-origin %s:\n%s", tc.devDefault, s)
			}
			if !strings.Contains(s, tc.replay) {
				t.Errorf("replay must be opt-in via %s:\n%s", tc.replay, s)
			}
			// No absolute collector URL baked in: the browser holds no
			// collector address, only a path on its own origin.
			if m := regexp.MustCompile(`https?://[^\s"'`+"`"+`]+`).FindAllString(
				stripTelemetryComments(s), -1); len(m) > 0 {
				t.Errorf("code (not comments) names an absolute URL %v — the bundle must hold no collector address", m)
			}
			for _, secret := range []string{"apiKey", "api_key", "authorization", "Bearer", "HYPERDX_API_KEY"} {
				if strings.Contains(stripTelemetryComments(s), secret) {
					t.Errorf("code mentions %q — the browser bundle must carry no credential (the runtime sends a public placeholder)", secret)
				}
			}
			if !strings.Contains(s, `"@reliantlabs/forge-web-runtime/telemetry"`) {
				t.Errorf("must import the SDK wiring from the /telemetry subpath:\n%s", s)
			}
			for _, want := range []string{"initTelemetry", "'shop-frontend'"} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q:\n%s", want, s)
				}
			}
		})
	}
}

// TestTelemetryDevProxyContract pins the /_otel proxy: same path, same
// upstream variable, same default, in both dev servers.
func TestTelemetryDevProxyContract(t *testing.T) {
	const upstreamEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	const defaultUpstream = "http://127.0.0.1:4318"

	for _, output := range []string{"static", "standalone", "server"} {
		t.Run("nextjs/"+output, func(t *testing.T) {
			s := renderTelemetryTmpl(t, "nextjs/next.config.ts.tmpl", telemetryData(output))

			for _, want := range []string{
				"process.env." + upstreamEnv,
				defaultUpstream,
				`source: "/_otel/:path*"`,
				"async rewrites()",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("next.config.ts (%s) missing %q", output, want)
				}
			}
			// Dev-only, so a static export does not carry a rewrite it
			// cannot honour — this is what keeps `forge lint --static-export`
			// quiet (RuleConfigRoutes reads exactly this gate).
			if !regexp.MustCompile(`process\.env\.NODE_ENV === "development"\s*\?\s*\{\s*async rewrites\(`).MatchString(s) {
				t.Errorf("next.config.ts (%s): the /_otel rewrite must be gated to NODE_ENV === \"development\"", output)
			}
		})
	}

	t.Run("vite-spa", func(t *testing.T) {
		s := renderTelemetryTmpl(t, "vite-spa/vite.config.ts.tmpl", telemetryData("static"))
		for _, want := range []string{
			"process.env." + upstreamEnv,
			defaultUpstream,
			`"/_otel": {`,
			`p.replace(/^\/_otel/, "")`,
			"changeOrigin: true",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("vite.config.ts missing %q", want)
			}
		}
	})
}

// TestTelemetryDependencies: the scaffolds install the exact-pinned SDK and
// not the eight @opentelemetry/* packages the retired ./otel wiring needed.
func TestTelemetryDependencies(t *testing.T) {
	for _, tmpl := range []string{
		"nextjs/package.json.tmpl",
		"vite-spa/package.json.tmpl",
	} {
		s := renderTelemetryTmpl(t, tmpl, telemetryData("static"))

		if !regexp.MustCompile(`"@hyperdx/browser": "\d+\.\d+\.\d+"`).MatchString(s) {
			t.Errorf("%s: @hyperdx/browser must be pinned to an exact version:\n%s", tmpl, s)
		}
		if !strings.Contains(s, `"@opentelemetry/api"`) {
			t.Errorf("%s: @opentelemetry/api is the runtime's required peer", tmpl)
		}
		for _, stale := range []string{
			"auto-instrumentations-web", "exporter-trace-otlp-http", "sdk-trace-web",
			"sdk-trace-base", "@opentelemetry/instrumentation", "@opentelemetry/resources",
			"@opentelemetry/core", "semantic-conventions",
		} {
			if strings.Contains(s, stale) {
				t.Errorf("%s still installs %s, which only the retired ./otel wiring used", tmpl, stale)
			}
		}
	}

	// React Native gets nothing: no DOM to instrument, and the browser SDK
	// would not even resolve there.
	rn := renderTelemetryTmpl(t, "react-native/package.json.tmpl", telemetryData("static"))
	if strings.Contains(rn, "@hyperdx") {
		t.Errorf("react-native must not install the browser SDK:\n%s", rn)
	}
}

// TestErrorSurfacesReportToTelemetry: error.tsx and global-error.tsx record
// through the runtime's reporter, fall back to console.error, and no longer
// point at Sentry.
func TestErrorSurfacesReportToTelemetry(t *testing.T) {
	for _, rel := range []string{
		"nextjs/src/app/error.tsx",
		"nextjs/src/app/global-error.tsx",
	} {
		s := renderTelemetryTmpl(t, rel, telemetryData("static"))
		if !strings.Contains(s, `import { reportException } from "@reliantlabs/forge-web-runtime"`) {
			t.Errorf("%s must import reportException from the runtime barrel:\n%s", rel, s)
		}
		if !strings.Contains(s, "reportException(error") {
			t.Errorf("%s must record the error with reportException:\n%s", rel, s)
		}
		if !strings.Contains(s, "console.error(error)") {
			t.Errorf("%s must fall back to console.error so an error is never silent:\n%s", rel, s)
		}
		if strings.Contains(strings.ToLower(s), "sentry") {
			t.Errorf("%s still mentions Sentry:\n%s", rel, s)
		}
	}
}

// TestProvidersInitTelemetryOnce: the Next shell starts telemetry through the
// generated module and no longer uses the retired client-RUM init.
func TestProvidersInitTelemetryOnce(t *testing.T) {
	s := renderTelemetryTmpl(t, "nextjs/src/app/providers.tsx.tmpl", telemetryData("static"))
	if !strings.Contains(s, `import { initTelemetry } from "@/lib/otel_gen"`) || !strings.Contains(s, "initTelemetry();") {
		t.Errorf("providers.tsx must call initTelemetry from @/lib/otel_gen:\n%s", s)
	}
	if strings.Contains(s, "initClientTelemetry") {
		t.Errorf("providers.tsx still uses the retired initClientTelemetry:\n%s", s)
	}

	m := renderTelemetryTmpl(t, "vite-spa/src/main.tsx", telemetryData("static"))
	if !strings.Contains(m, `import { initTelemetry } from "@/lib/otel_gen"`) || !strings.Contains(m, "initTelemetry();") {
		t.Errorf("main.tsx must start telemetry:\n%s", m)
	}
}

// stripComments removes // line comments and /* */ blocks so an assertion
// about CODE is not tripped by prose that explains the design.
func stripTelemetryComments(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, "")
	return regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(s, "")
}

// TestTelemetryConfigWiresTheFrontendKnobs pins what `forge.Frontend.telemetry`
// and the frontend's identity reach the browser through: the frontend's name
// (the forge.frontend resource attribute that joins a page's telemetry to its
// source maps) is baked in, and console_capture / replay_sample_rate are read
// from the runtime config document — never from a build variable, which would
// freeze them into one environment's bundle.
func TestTelemetryConfigWiresTheFrontendKnobs(t *testing.T) {
	for name, tmpl := range map[string]string{
		"nextjs":   "nextjs/src/lib/otel_gen.ts.tmpl",
		"vite-spa": "vite-spa/src/lib/otel_gen.ts.tmpl",
	} {
		t.Run(name, func(t *testing.T) {
			data := telemetryData("static")
			data.FrontendName = "storefront"
			raw := renderTelemetryTmpl(t, tmpl, data)
			s := stripTelemetryComments(raw)

			for _, want := range []string{
				`frontend: 'storefront'`,
				`consoleCapture: text(cfg.CONSOLE_CAPTURE) !== "false"`,
				`replaySampleRate: rate(cfg.REPLAY_SAMPLE_RATE)`,
			} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q:\n%s", want, s)
				}
			}
			for _, buildVar := range []string{"CONSOLE_CAPTURE", "REPLAY_SAMPLE_RATE"} {
				if regexp.MustCompile(`(process\.env|import\.meta\.env)\.[A-Z_]*` + buildVar).MatchString(raw) {
					t.Errorf("%s is read from a build variable; it must come from config.js so one build promotes across envs", buildVar)
				}
			}
			// An unset or malformed rate must reach the runtime as undefined
			// ("every session"), never as NaN or 0 (which would record nobody).
			if !strings.Contains(raw, `text(value) !== "" && Number.isFinite(n) ? n : undefined`) {
				t.Errorf("a malformed REPLAY_SAMPLE_RATE must become undefined:\n%s", raw)
			}
		})
	}
}

// TestTelemetryHeaderNamesTheHostedDefault keeps the explanation honest: a
// hosted frontend gets /_otel from forge, and the comment says so.
func TestTelemetryHeaderNamesTheHostedDefault(t *testing.T) {
	for _, tmpl := range []string{"nextjs/src/lib/otel_gen.ts.tmpl", "vite-spa/src/lib/otel_gen.ts.tmpl"} {
		s := renderTelemetryTmpl(t, tmpl, telemetryData("static"))
		if !strings.Contains(s, "forge.OnHosted") || !strings.Contains(s, `OTEL_ENDPOINT = "/_otel"`) {
			t.Errorf("%s: the header must say a forge.OnHosted frontend defaults OTEL_ENDPOINT to /_otel", tmpl)
		}
		if strings.Contains(s, "must route") && !strings.Contains(s, "any other") {
			t.Errorf("%s: the ingress paragraph must be scoped to the non-hosted runtimes", tmpl)
		}
	}
}
