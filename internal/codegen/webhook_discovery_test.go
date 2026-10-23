package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestWebhookNamesForService_IgnoresItsOwnOutput pins the self-discovery bug:
// forge writes webhook_routes_gen.go into the very directory it scans for
// webhook_*.go, so the second `forge generate` used to find a phantom webhook
// named "routes_gen" and emit a route for a handler nothing declares —
// "s.handleWebhookRoutesGen undefined", with the whole generate run rolling
// back. It only surfaced once `forge scaffold webhook` became reachable on
// a fresh project.
func TestWebhookNamesForService_IgnoresItsOwnOutput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"webhook_stripe.go",
		"webhook_github.go",
		"webhook_stripe_test.go", // tests are not declarations
		"webhook_store.go",       // the shared idempotency store
		"webhook_routes_gen.go",  // forge's OWN output for this scan
		"service.go",             // not a webhook at all
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package widget\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := WebhookNamesForService(dir)
	want := []string{"github", "stripe"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WebhookNamesForService = %v, want %v", got, want)
	}
	if !ServiceHasWebhooks(dir) {
		t.Error("ServiceHasWebhooks should be true for a directory with real webhooks")
	}

	// A directory carrying ONLY forge's own output declares no webhooks.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "webhook_routes_gen.go"), []byte("package widget\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if names := WebhookNamesForService(empty); len(names) != 0 {
		t.Fatalf("WebhookNamesForService on a gen-file-only dir = %v, want none", names)
	}
}
