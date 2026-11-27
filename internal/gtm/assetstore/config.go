// Copyright (c) 2026 Reliant Labs

package assetstore

import (
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/blobstore"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// StoreFromConfig opens the bucket cfg names (ServerProcessConfig.gtm_assets,
// the leaves forge.bucket_env("GTM_ASSETS", "gtm-assets") fills; PR-12 reads
// it as g.ServerProcessConfig.GetGtmAssets(), which is what makes the API
// server and the worker reach it). With no bucket it is nil and no error:
// GTM assets are off, and New(nil) keeps them off. A bucket without its
// endpoint or credentials is an error (the config's own validation refuses
// it at load too).
func StoreFromConfig(cfg *configv1.GtmAssetsConfig) (blobstore.Store, error) {
	if cfg.GetBucket() == "" {
		return nil, nil
	}
	if cfg.GetEndpoint() == "" || cfg.GetAccessKeyId() == "" || cfg.GetSecretAccessKey() == "" {
		return nil, errors.New("assetstore: GTM_ASSETS_BUCKET needs GTM_ASSETS_ENDPOINT, GTM_ASSETS_ACCESS_KEY_ID and GTM_ASSETS_SECRET_ACCESS_KEY")
	}
	s3, err := blobstore.NewS3(blobstore.S3Config{
		Endpoint:        cfg.GetEndpoint(),
		PublicEndpoint:  cfg.GetPublicEndpoint(),
		Region:          cfg.GetRegion(),
		Bucket:          cfg.GetBucket(),
		AccessKeyID:     cfg.GetAccessKeyId(),
		SecretAccessKey: cfg.GetSecretAccessKey(),
		PathStyle:       cfg.GetPathStyle(),
	})
	if err != nil {
		return nil, fmt.Errorf("assetstore: open the GTM assets bucket: %w", err)
	}
	return s3, nil
}
