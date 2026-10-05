// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupConfigDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("RELIANT_USER_CONFIG_DIR", dir)
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func callSetEndpoints(t *testing.T, payload string) ([]byte, error) {
	t.Helper()
	return handleLocalModelsSetEndpoints(context.Background(), []byte(payload))
}

func readConfig(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const localCfgFixture = `# my settings
theme: dark   # keep me
models:
  # custom models live here
  custom:
    - id: foo
  providers:
    openai:
      key: abc
    local:
      base_url: http://localhost:11434/v1
      api_key: secret
other: 1
`

func TestLocalModelSetEndpointsRoundTripKeepsKeysAndComments(t *testing.T) {
	dir := setupConfigDir(t, localCfgFixture)
	out, err := callSetEndpoints(t, `{"base_urls":["http://gpu-box.lan:8000/v1/","http://gpu-box.lan:8000/v1","http://localhost:11434/v1"]}`)
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("%s %v", out, err)
	}
	got := readConfig(t, dir)
	for _, want := range []string{"# my settings", "theme: dark", "# keep me", "# custom models live here", "- id: foo", "key: abc", "other: 1", "api_key: secret"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "base_url:") {
		t.Errorf("legacy base_url not removed:\n%s", got)
	}
	cfg := parseLocalModelsConfig([]byte(got))
	eps := cfg.endpoints()
	if len(eps) != 2 || eps[0].BaseURL != "http://gpu-box.lan:8000/v1" || eps[1].BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("endpoints = %+v", eps)
	}
	if eps[0].APIKey != "secret" { // top-level key is inherited
		t.Errorf("api key lost: %+v", eps)
	}
	if st, _ := os.Stat(filepath.Join(dir, "config.yaml")); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
}

func TestLocalModelSetEndpointsFoldsLegacyBaseURL(t *testing.T) {
	dir := setupConfigDir(t, "models:\n  providers:\n    local:\n      base_url: http://localhost:1234/v1\n")
	if _, err := callSetEndpoints(t, `{"base_urls":["http://localhost:1234/v1","http://box:8000"]}`); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, dir)
	if strings.Contains(got, "base_url:") || strings.Count(got, "localhost:1234") != 1 {
		t.Errorf("legacy not folded into list:\n%s", got)
	}
	if n := len(parseLocalModelsConfig([]byte(got)).endpoints()); n != 2 {
		t.Errorf("endpoints = %d", n)
	}
}

func TestLocalModelSetEndpointsInvalidLeavesFileUnchanged(t *testing.T) {
	dir := setupConfigDir(t, localCfgFixture)
	for _, bad := range []string{
		`{"base_urls":["http://ok:1","ftp://x"]}`,
		`{"base_urls":["http://ok:1","http://user:pw@host:1"]}`,
		`{"base_urls":["http://ok:1","http://"]}`,
		`{"base_urls":["http://ok:1","not a url"]}`,
		`{"base_urls":["http://ok:1",""]}`,
		`not json`,
	} {
		if _, err := callSetEndpoints(t, bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
	var many []string
	for i := 0; i < 17; i++ {
		many = append(many, "http://h"+string(rune('a'+i))+":1")
	}
	if _, err := validateLocalEndpointURLs(many); err == nil {
		t.Error("17 endpoints should be rejected")
	}
	if got := readConfig(t, dir); got != localCfgFixture {
		t.Errorf("file changed:\n%s", got)
	}
}

func TestLocalModelSetEndpointsEmptyClears(t *testing.T) {
	dir := setupConfigDir(t, localCfgFixture)
	if _, err := callSetEndpoints(t, `{"base_urls":[]}`); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t, dir)
	if n := len(parseLocalModelsConfig([]byte(got)).endpoints()); n != 0 {
		t.Errorf("endpoints remain:\n%s", got)
	}
	if !strings.Contains(got, "# my settings") || !strings.Contains(got, "key: abc") {
		t.Errorf("unrelated content lost:\n%s", got)
	}
}

func TestLocalModelSetEndpointsCreatesFileAndLeavesNoTemp(t *testing.T) {
	dir := setupConfigDir(t, "")
	if _, err := callSetEndpoints(t, `{"base_urls":["http://a:1"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callSetEndpoints(t, `{"base_urls":["http://a:1","http://b:2"]}`); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Errorf("dir contents: %v", entries)
	}
	if n := len(parseLocalModelsConfig([]byte(readConfig(t, dir))).endpoints()); n != 2 {
		t.Errorf("endpoints = %d", n)
	}
}

func TestLocalModelSetEndpointsKeepsPerEndpointKeyAndTriggersProbe(t *testing.T) {
	dir := setupConfigDir(t, "models:\n  providers:\n    local:\n      base_urls:\n        - {base_url: \"http://a:1\", api_key: k1}\n")
	m := newLocalModelManager()
	activeLocalModels.Store(m)
	t.Cleanup(func() { activeLocalModels.Store(nil) })
	if _, err := callSetEndpoints(t, `{"base_urls":["http://a:1/","http://b:2"]}`); err != nil {
		t.Fatal(err)
	}
	eps := parseLocalModelsConfig([]byte(readConfig(t, dir))).endpoints()
	if len(eps) != 2 || eps[0].APIKey != "k1" || eps[0].BaseURL != "http://a:1" {
		t.Errorf("endpoints = %+v", eps)
	}
	select {
	case <-m.refresh:
	default:
		t.Error("probe not triggered")
	}
}
