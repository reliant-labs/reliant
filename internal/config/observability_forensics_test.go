package config

import "testing"

// observability.forensics defaults on; only an explicit false turns the
// generated forensics capture off.
func TestObservabilityForensics(t *testing.T) {
	for src, want := range map[string]bool{
		"name: shop\n": true,
		"name: shop\nobservability: {forensics: true}\n":  true,
		"name: shop\nobservability: {forensics: false}\n": false,
		"name: shop\nobservability: {log_level: info}\n":  true,
	} {
		cfg, err := loadIn(t, src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if got := cfg.Observability.ForensicsEnabled(); got != want {
			t.Errorf("%q: ForensicsEnabled() = %v, want %v", src, got, want)
		}
	}
}
