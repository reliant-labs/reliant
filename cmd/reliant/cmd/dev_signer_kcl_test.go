// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"os"
	"path/filepath"
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

// TestDevSignInFlagRenderRefusedOutsideDev pins the render refusal of the web
// app's dev sign-in build flag: deploy/kcl/dev_sign_in.k's only_in_dev refuses
// a bundle outside dev whose frontend sets VITE_DEV_SIGN_IN, and renders an
// ordinary prod bundle and the dev bundle that sets it.
func TestDevSignInFlagRenderRefusedOutsideDev(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var dev string
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/dev_sign_in_render.k", "dev", &dev, project),
		"the dev env renders the dev sign-in flag")
	require.Equal(t, "true", dev)

	var prodWithout string
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/dev_sign_in_render.k", "prod_without", &prodWithout, project),
		"an ordinary prod frontend renders")
	require.Equal(t, "web", prodWithout)

	var refused string
	err := kcleval.Select(context.Background(), "deploy/kcl/testdata/dev_sign_in_render_prod.k", "prod", &refused, project)
	require.Error(t, err, "a prod frontend rendered with VITE_DEV_SIGN_IN")
	require.True(t, strings.Contains(err.Error(), "VITE_DEV_SIGN_IN is a dev-only build flag"),
		"refused, but not by the dev sign-in check: %v", err)
}

// TestEveryEnvRendersThroughDevSignInGuard holds every env of this repo to
// dev_sign_in.k: each deploy/kcl/<env>/main.k renders its bundle through
// only_in_dev under its own name, so no env can declare VITE_DEV_SIGN_IN on a
// frontend without the render check seeing it.
func TestEveryEnvRendersThroughDevSignInGuard(t *testing.T) {
	mains, err := filepath.Glob("../../../deploy/kcl/*/main.k")
	require.NoError(t, err)
	require.NotEmpty(t, mains)
	for _, main := range mains {
		env := filepath.Base(filepath.Dir(main))
		src, err := os.ReadFile(main)
		require.NoError(t, err)
		require.Contains(t, string(src), `forge.render(dsi.only_in_dev("`+env+`", `,
			"deploy/kcl/%s/main.k must render through dev_sign_in.k's only_in_dev(%q, ...)", env, env)
	}
}
