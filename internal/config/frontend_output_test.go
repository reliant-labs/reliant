package config

import "testing"

// TestFrontendStaticExport pins what an unset or mixed-case `output:` means,
// and that only a Next.js frontend can have a non-static build. The
// static-export lint and the render-time refusal of a server build on a
// static runtime both read the build shape through these methods, so a
// disagreement between them and the scaffolder would become a refusal of the
// scaffold's own output.
func TestFrontendStaticExport(t *testing.T) {
	cases := []struct {
		name       string
		fe         FrontendConfig
		wantOutput string
		wantStatic bool
	}{
		{"unset nextjs is the legacy standalone shape", FrontendConfig{Type: "nextjs"}, FrontendOutputStandalone, false},
		{"empty type is nextjs", FrontendConfig{}, FrontendOutputStandalone, false},
		{"declared static", FrontendConfig{Type: "nextjs", Output: "static"}, FrontendOutputStatic, true},
		{"declared static, any case and padding", FrontendConfig{Type: "nextjs", Output: " Static "}, FrontendOutputStatic, true},
		{"declared standalone", FrontendConfig{Type: "nextjs", Output: "standalone"}, FrontendOutputStandalone, false},
		{"declared server", FrontendConfig{Type: "nextjs", Output: "server"}, FrontendOutputServer, false},
		{"vite spa is static whatever output says", FrontendConfig{Type: "vite-spa", Output: "standalone"}, FrontendOutputStandalone, true},
		{"react-native web build is static", FrontendConfig{Type: "react-native"}, FrontendOutputStandalone, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fe.EffectiveOutput(); got != tc.wantOutput {
				t.Errorf("EffectiveOutput() = %q, want %q", got, tc.wantOutput)
			}
			if got := tc.fe.StaticExport(); got != tc.wantStatic {
				t.Errorf("StaticExport() = %v, want %v", got, tc.wantStatic)
			}
		})
	}
}
