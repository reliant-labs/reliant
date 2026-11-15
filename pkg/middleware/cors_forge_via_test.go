package middleware_test

import (
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/middleware"
)

// A browser console names itself in Forge-Via; a header the preflight does
// not admit makes the browser refuse every RPC, so the default allow list
// must carry the exact header the activity interceptor reads.
func TestDefaultCORSAllowHeaders_AdmitForgeVia(t *testing.T) {
	if !slices.Contains(middleware.DefaultCORSAllowHeaders, activity.ViaHeader) {
		t.Errorf("DefaultCORSAllowHeaders = %v, want it to include %q", middleware.DefaultCORSAllowHeaders, activity.ViaHeader)
	}
}
