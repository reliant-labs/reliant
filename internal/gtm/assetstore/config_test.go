// Copyright (c) 2026 Reliant Labs

package assetstore_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
	"github.com/reliant-labs/reliant/pkg/config"
)

// bucketEnv is what forge.bucket_env("GTM_ASSETS", "gtm-assets") renders
// for the dev MinIO.
var bucketEnv = map[string]string{
	"GTM_ASSETS_ENDPOINT":          "http://localhost:21090",
	"GTM_ASSETS_PUBLIC_ENDPOINT":   "http://localhost:21090",
	"GTM_ASSETS_REGION":            "us-east-1",
	"GTM_ASSETS_BUCKET":            "gtm-assets",
	"GTM_ASSETS_ACCESS_KEY_ID":     "minioadmin",
	"GTM_ASSETS_SECRET_ACCESS_KEY": "minioadmin",
	"GTM_ASSETS_PATH_STYLE":        "true",
}

// load loads the server roles' config, where the bucket is, from env alone.
func load(t *testing.T, env map[string]string) (*config.ServerProcessConfig, error) {
	t.Helper()
	for k := range bucketEnv {
		t.Setenv(k, "")
	}
	t.Setenv("NATS_URL", "nats://127.0.0.1:4222") // required by every server role
	for k, v := range env {
		t.Setenv(k, v)
	}
	cmd := &cobra.Command{Use: "test"}
	config.RegisterServerProcessConfigFlags(cmd)
	return config.LoadServerProcessConfig(cmd)
}

func TestConfigDefaultIsOff(t *testing.T) {
	cfg, err := load(t, nil)
	require.NoError(t, err)

	store, err := assetstore.StoreFromConfig(cfg.GetGtmAssets())
	require.NoError(t, err)
	assert.Nil(t, store, "no bucket: assets are off")
	assert.Nil(t, assetstore.New(store))
}

func TestConfigFromBucketEnv(t *testing.T) {
	cfg, err := load(t, bucketEnv)
	require.NoError(t, err)
	a := cfg.GetGtmAssets()
	assert.Equal(t, "gtm-assets", a.GetBucket())
	assert.True(t, a.GetPathStyle())

	store, err := assetstore.StoreFromConfig(a)
	require.NoError(t, err)
	require.IsType(t, &blobstore.S3{}, store)
	assert.NotNil(t, assetstore.New(store))
}

func TestConfigRefusesAnIncompleteBucket(t *testing.T) {
	for _, missing := range []string{"GTM_ASSETS_ENDPOINT", "GTM_ASSETS_ACCESS_KEY_ID", "GTM_ASSETS_SECRET_ACCESS_KEY"} {
		t.Run(missing, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range bucketEnv {
				if k != missing {
					env[k] = v
				}
			}
			_, err := load(t, env)
			require.Error(t, err, "refused at load")
			assert.Contains(t, err.Error(), "GTM_ASSETS_BUCKET requires")
		})
	}
}
