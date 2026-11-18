// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// TestDevSignerRenderRefusedOutsideDev pins the render half of dev sign-in's
// refusal in reliant's own KCL: deploy/kcl/workloads.k's api_server (which
// every env of this repo and of control-plane binds) refuses
// AUTH_DEV_JWT_SECRET in an env whose tier is not dev — prod, or no
// RELIANT_ENV at all — and renders it in dev. The process refuses it again at
// start (TestServerAPIRefusesDevSignerOutsideDev).
func TestDevSignerRenderRefusedOutsideDev(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var dev map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/dev_signer_render.k", "dev", &dev, project),
		"the dev tier renders the dev signer")
	require.Equal(t, "dev-local-hmac-shared-secret-do-not-use-in-prod", dev["AUTH_DEV_JWT_SECRET"])

	var prodWithout map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/dev_signer_render.k", "prod_without", &prodWithout, project),
		"prod without the dev signer renders")

	for fixture, selector := range map[string]string{
		"deploy/kcl/testdata/dev_signer_render_prod.k":  "prod",
		"deploy/kcl/testdata/dev_signer_render_unset.k": "unset",
	} {
		var refused map[string]any
		err := kcleval.Select(context.Background(), fixture, selector, &refused, project)
		require.Error(t, err, "%s: the api-server rendered with AUTH_DEV_JWT_SECRET outside the dev tier", fixture)
		require.True(t, strings.Contains(err.Error(), "AUTH_DEV_JWT_SECRET is a dev-only signer"),
			"%s: refused, but not by the dev signer check: %v", fixture, err)
	}
}
