package templates

import (
	"strings"
	"testing"
)

// TestE2EMainWaitsOnReadyz pins obs 968366bc: the scaffolded e2e TestMain
// waited on /healthz, which the serve command's boot probes answer 200 before
// migrations run and the API listener is up, so the first RPC could reach the
// boot listener and fail "unimplemented: 404 Not Found". It waits on /readyz,
// which answers 503 until the API listener owns the address.
func TestE2EMainWaitsOnReadyz(t *testing.T) {
	t.Parallel()
	data, err := templateFS.ReadFile("test/e2e/main_test.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, `base + "/readyz"`) {
		t.Fatal(`e2e main_test.go.tmpl must poll base + "/readyz" before running tests`)
	}
	if strings.Contains(src, `base + "/healthz"`) {
		t.Fatal(`e2e main_test.go.tmpl polls /healthz, which the boot probes answer 200 before the API is up`)
	}
}
