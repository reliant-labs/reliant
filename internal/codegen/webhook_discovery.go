package codegen

import (
	"os"
	"sort"
	"strings"
)

// WebhookNamesForService discovers a service's webhooks from the REAL
// source — the webhook_<name>.go handler files under its directory — rather
// than a declared config list. `forge scaffold webhook` scaffolds
// webhook_<name>.go (+ _test.go); the file IS the declaration, so nothing
// needs to cache the name in forge.yaml or a components manifest. This
// mirrors how forge discovers services themselves.
//
// handlerDir is the service's on-disk directory (e.g. internal/handlers/foo,
// resolved via ResolveServiceComponent). Returns the webhook names sorted for
// stable codegen. Best-effort: a missing directory yields nil.
//
// Three exclusions, and two are load-bearing. forge's OWN output for this
// discovery is webhook_routes_gen.go, which sits in the same directory and
// matches the same webhook_*.go prefix. Without the *_gen.go exclusion the
// second `forge generate` discovers a phantom webhook named "routes_gen" and
// emits a route for s.handleWebhookRoutesGen, which nothing declares:
//
//	internal/handlers/widget/webhook_routes_gen.go:17:67:
//	    s.handleWebhookRoutesGen undefined
//
// webhook_store.go is excluded for the same reason on a longer timescale: the
// dedupe store now lives in forge/pkg/middleware and is no longer scaffolded,
// but projects generated before that still carry the file on disk, and it
// would otherwise be read as a webhook named "store".
func WebhookNamesForService(handlerDir string) []string {
	entries, err := os.ReadDir(handlerDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, "webhook_") || !strings.HasSuffix(n, ".go") {
			continue
		}
		if n == "webhook_store.go" || strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimPrefix(n, "webhook_"), ".go"))
	}
	sort.Strings(names)
	return names
}

// ServiceHasWebhooks reports whether a service's directory carries any
// webhook_<name>.go handler file.
func ServiceHasWebhooks(handlerDir string) bool {
	return len(WebhookNamesForService(handlerDir)) > 0
}
