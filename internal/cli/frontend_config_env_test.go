package cli

import "testing"

// TestFrontendConfigEnv_FrameworkPrefixes pins the config→env mapping per
// frontend type: each typed knob lands on the framework-prefixed variable
// the scaffold's transport reads, and NEXT_TELEMETRY_DISABLED is Next-only.
func TestFrontendConfigEnv_FrameworkPrefixes(t *testing.T) {
	cfg := &FrontendConfigEntity{
		APIURL:            "https://api.example.com",
		Mock:              "off",
		OTELEndpoint:      "https://otel.example.com",
		Environment:       "staging",
		TelemetryDisabled: true,
	}

	cases := []struct {
		typ  string
		want map[string]string
	}{
		{
			typ: "nextjs",
			want: map[string]string{
				"NEXT_PUBLIC_API_URL":       "https://api.example.com",
				"NEXT_PUBLIC_OTEL_ENDPOINT": "https://otel.example.com",
				"NEXT_PUBLIC_ENVIRONMENT":   "staging",
				"NEXT_TELEMETRY_DISABLED":   "1",
			},
		},
		{
			// "vite" is the KCL Frontend.type spelling render.k projects;
			// the dispatch must map it to VITE_ (not the NEXT_PUBLIC_
			// default), same as the longer "vite-spa" scaffold kind.
			typ: "vite",
			want: map[string]string{
				"VITE_API_URL":       "https://api.example.com",
				"VITE_OTEL_ENDPOINT": "https://otel.example.com",
				"VITE_ENVIRONMENT":   "staging",
			},
		},
		{
			typ: "vite-spa",
			want: map[string]string{
				"VITE_API_URL":       "https://api.example.com",
				"VITE_OTEL_ENDPOINT": "https://otel.example.com",
				"VITE_ENVIRONMENT":   "staging",
			},
		},
		{
			// "rn" is the KCL spelling; "react-native" the scaffold kind.
			typ: "rn",
			want: map[string]string{
				"EXPO_PUBLIC_API_URL":       "https://api.example.com",
				"EXPO_PUBLIC_OTEL_ENDPOINT": "https://otel.example.com",
				"EXPO_PUBLIC_ENVIRONMENT":   "staging",
			},
		},
		{
			typ: "react-native",
			want: map[string]string{
				"EXPO_PUBLIC_API_URL":       "https://api.example.com",
				"EXPO_PUBLIC_OTEL_ENDPOINT": "https://otel.example.com",
				"EXPO_PUBLIC_ENVIRONMENT":   "staging",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			got := map[string]string{}
			for _, ev := range frontendConfigEnv(tc.typ, cfg) {
				got[ev.Name] = ev.Value
			}
			// mock=off contributes nothing (real backend is the default).
			if _, ok := got["NEXT_PUBLIC_MOCK_API"]; ok {
				t.Error("mock=off must not emit a *_MOCK_API var in frontendConfigEnv")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("var count: got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestFrontendConfigEnv_MockPassthrough confirms a config mock=true/hybrid
// surfaces the *_MOCK_API var at dev-launch (overridable by the shell),
// while mock=off is silent there.
func TestFrontendConfigEnv_MockPassthrough(t *testing.T) {
	for _, mode := range []string{"true", "hybrid"} {
		vars := frontendConfigEnv("nextjs", &FrontendConfigEntity{Mock: mode})
		found := ""
		for _, ev := range vars {
			if ev.Name == "NEXT_PUBLIC_MOCK_API" {
				found = ev.Value
			}
		}
		if found != mode {
			t.Errorf("mock=%q: want NEXT_PUBLIC_MOCK_API=%q, got %q", mode, mode, found)
		}
	}
}

// TestEffectiveEnvVars_ExplicitWins confirms an explicit env_vars entry
// overrides the config-derived value for the same variable, and that a
// frontend with no config block returns its env_vars unchanged.
func TestEffectiveEnvVars_ExplicitWins(t *testing.T) {
	fe := FrontendEntity{
		Name: "web",
		Type: "nextjs",
		Config: &FrontendConfigEntity{
			APIURL:            "https://config.example.com",
			TelemetryDisabled: false,
		},
		EnvVars: []KCLEnvVar{
			{Name: "NEXT_PUBLIC_API_URL", Value: "https://explicit.example.com"},
			{Name: "NEXT_PUBLIC_SUPABASE_URL", Value: "https://sb"},
		},
	}
	got := map[string]string{}
	for _, ev := range fe.EffectiveEnvVars() {
		got[ev.Name] = ev.Value
	}
	if got["NEXT_PUBLIC_API_URL"] != "https://explicit.example.com" {
		t.Errorf("explicit env_vars must win: got %q", got["NEXT_PUBLIC_API_URL"])
	}
	if got["NEXT_PUBLIC_SUPABASE_URL"] != "https://sb" {
		t.Error("non-colliding explicit env_vars must pass through")
	}

	// No config block: env_vars returned unchanged.
	plain := FrontendEntity{Type: "nextjs", EnvVars: []KCLEnvVar{{Name: "X", Value: "1"}}}
	if ev := plain.EffectiveEnvVars(); len(ev) != 1 || ev[0].Name != "X" {
		t.Errorf("no-config frontend must return env_vars unchanged, got %+v", ev)
	}
}

// TestFrontendBuildEnv_ForcesMock pins the structural mock-leak fix: the
// build env always carries the *_MOCK_API var, force-set to config.mock
// (empty for the default off), regardless of what env_vars declare.
func TestFrontendBuildEnv_ForcesMock(t *testing.T) {
	// Default (no config): mock var is present-but-empty so it overrides a
	// shell NEXT_PUBLIC_MOCK_API=true downstream via MergeExtraWins.
	be := frontendBuildEnv(FrontendEntity{Type: "nextjs"})
	if v, ok := be["NEXT_PUBLIC_MOCK_API"]; !ok || v != "" {
		t.Errorf("default build env must force NEXT_PUBLIC_MOCK_API=\"\", got %q (present=%v)", v, ok)
	}

	// An explicit env_vars mock is overridden by the force (empty for off).
	be = frontendBuildEnv(FrontendEntity{
		Type:    "vite-spa",
		EnvVars: []KCLEnvVar{{Name: "VITE_MOCK_API", Value: "true"}},
	})
	if be["VITE_MOCK_API"] != "" {
		t.Errorf("build env must force the mock var to off, got %q", be["VITE_MOCK_API"])
	}
}

// TestCheckDeployableFrontendMock hard-errors a deployable frontend whose
// config declares a non-off mock; off (or no config) is allowed.
func TestCheckDeployableFrontendMock(t *testing.T) {
	if err := checkDeployableFrontendMock(FrontendEntity{Name: "web", Type: "nextjs"}); err != nil {
		t.Errorf("no-config frontend must be allowed, got %v", err)
	}
	if err := checkDeployableFrontendMock(FrontendEntity{
		Name: "web", Type: "nextjs", Config: &FrontendConfigEntity{Mock: "off"},
	}); err != nil {
		t.Errorf("mock=off must be allowed, got %v", err)
	}
	if err := checkDeployableFrontendMock(FrontendEntity{
		Name: "web", Type: "nextjs", Config: &FrontendConfigEntity{Mock: "true"},
	}); err == nil {
		t.Error("mock=true on a deployable frontend must hard-error")
	}
}
