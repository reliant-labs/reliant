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
			replay:     `replay: replay === "true" && replayPrivacyAllows(),`,
		},
		"vite-spa": {
			tmpl:       "vite-spa/src/lib/otel_gen.ts.tmpl",
			devDefault: `"/_otel"`,
			replay:     `replay: replay === "true" && replayPrivacyAllows(),`,
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
			for _, want := range []string{"initTelemetry", "export const SERVICE_NAME = 'shop-web';", "serviceName: SERVICE_NAME,"} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q:\n%s", want, s)
				}
			}
		})
	}
}

// TestTelemetryPerEnvValuesPreferRuntimeConfig pins the promotion contract
// in the source: a value that differs per environment (the ingest endpoint,
// the environment name, session replay) is read from the runtime config
// document first, through runtimeOrBuild, and its build variable is only the
// DEPRECATED fallback — written out verbatim, since the bundler inlines
// nothing else, and announced by one console.warn. The behaviour of each path
// is TestTelemetryBuildVariableFallbackBehaviour.
func TestTelemetryPerEnvValuesPreferRuntimeConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		tmpl, prefix, read string
	}{
		"nextjs":   {tmpl: "nextjs/src/lib/otel_gen.ts.tmpl", prefix: "NEXT_PUBLIC_", read: "process.env."},
		"vite-spa": {tmpl: "vite-spa/src/lib/otel_gen.ts.tmpl", prefix: "VITE_", read: "import.meta.env."},
	} {
		t.Run(name, func(t *testing.T) {
			// Raw, not comment-stripped: the header's `/_otel/*` opens what
			// stripTelemetryComments takes for a block comment.
			s := renderTelemetryTmpl(t, tc.tmpl, telemetryData("static"))
			for key, field := range map[string]string{
				"OTEL_ENDPOINT": `"config.otel_endpoint"`,
				"ENVIRONMENT":   `"config.environment"`,
				"OTEL_REPLAY":   `'runtime_config.OTEL_REPLAY = "true"'`,
			} {
				build := tc.prefix + key
				re := regexp.MustCompile(`runtimeOrBuild\(\s*used,\s*cfg\.` + key + `,\s*"` + build +
					`",\s*` + regexp.QuoteMeta(tc.read+build) + `[^,]*,\s*` + regexp.QuoteMeta(field) + `,\s*\)`)
				if !re.MatchString(s) {
					t.Errorf("%s must be read runtime-first through runtimeOrBuild, with %s%s as the deprecated fallback naming %s:\n%s",
						key, tc.read, build, field, s)
				}
				if n := strings.Count(s, tc.read+build); n != 1 {
					t.Errorf("%s%s is read %d times; only the deprecated fallback may read it", tc.read, build, n)
				}
			}
			for _, want := range []string{
				"warnBuildFallback(used);",
				"console.warn(",
				"next major version of forge",
				"environment: environment || undefined,",
				`replay: replay === "true" && replayPrivacyAllows(),`,
			} {
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
			"forgeOtelProxy({ upstream: OTEL_ENDPOINT })",
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

// TestTelemetryDevProxyHostedToken pins who does the /_otel proxy's work: the
// runtime (@reliantlabs/forge-web-runtime's otel-proxy, otel-proxy.test.ts),
// which forwards only `POST /v1/{traces,logs,metrics}` under 8 MiB, attaches
// FORGE_OTEL_HEADERS_FILE's token per request and drops the page's cookies
// and authorization. Both dev servers must route /_otel through it and carry
// no hand-rolled forwarding of their own: a config-file copy is exactly the
// unfiltered, unbounded relay that let anything reaching the dev server post
// arbitrary routes to the hosted edge as the stack.
func TestTelemetryDevProxyHostedToken(t *testing.T) {
	handRolled := []string{
		"readOtelHeaders",
		"http.createServer",
		"proxyReq",
		"node:http",
		"node:https",
		"FORGE_OTEL_HEADERS_FILE",
	}
	for _, output := range []string{"static", "standalone", "server"} {
		t.Run("nextjs/"+output, func(t *testing.T) {
			s := renderTelemetryTmpl(t, "nextjs/next.config.ts.tmpl", telemetryData(output))
			for _, want := range []string{
				`import { startOtelRelay } from "@reliantlabs/forge-web-runtime/otel-proxy";`,
				"startOtelRelay({ upstream: otelUpstream })",
				"const otel = await devOtelUpstream();",
				// No relay, no route: never an unfiltered rewrite to the upstream.
				"...(otel ? [{ source: \"/_otel/:path*\", destination: `${otel}/:path*` }] : [])",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("next.config.ts (%s) missing %q", output, want)
				}
			}
			code := stripTelemetryComments(s)
			for _, stale := range handRolled {
				if strings.Contains(code, stale) {
					t.Errorf("next.config.ts (%s) still hand-rolls the /_otel relay (%q); it must be the runtime's startOtelRelay", output, stale)
				}
			}
			if strings.Contains(code, "destination: `${otelUpstream}") {
				t.Errorf("next.config.ts (%s) rewrites /_otel straight to the upstream, past the relay's rules", output)
			}
		})
	}
	t.Run("vite-spa", func(t *testing.T) {
		s := renderTelemetryTmpl(t, "vite-spa/vite.config.ts.tmpl", telemetryData("static"))
		for _, want := range []string{
			`import { forgeOtelProxy, forgeRuntimeConfig } from "@reliantlabs/forge-web-runtime/vite";`,
			"forgeOtelProxy({ upstream: OTEL_ENDPOINT }),",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("vite.config.ts missing %q", want)
			}
		}
		code := stripTelemetryComments(s)
		for _, stale := range handRolled {
			if strings.Contains(code, stale) {
				t.Errorf("vite.config.ts still hand-rolls the /_otel proxy (%q); it must be the runtime's forgeOtelProxy", stale)
			}
		}
		// A server.proxy entry would forward every path and method; the
		// plugin shadows one, but the scaffold must not ship it.
		if strings.Contains(code, `"/_otel": {`) {
			t.Errorf("vite.config.ts still declares a server.proxy /_otel entry")
		}
	})
}
