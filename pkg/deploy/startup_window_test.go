package deploy_test

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// A forge server waits up to DefaultDBConnectTimeout for its database while
// reporting not-ready. If the rendered startup probe gave up sooner, the
// kubelet would kill the pod mid-wait and the retry would be a crash loop
// again.
func TestStartupWindowCoversDBConnectTimeout(t *testing.T) {
	window := time.Duration(v1alpha1.DefaultStartupPeriodSeconds*v1alpha1.DefaultStartupFailureThreshold) * time.Second
	if window <= serverkit.DefaultDBConnectTimeout {
		t.Fatalf("startup probe window %s does not cover DefaultDBConnectTimeout %s", window, serverkit.DefaultDBConnectTimeout)
	}
}
