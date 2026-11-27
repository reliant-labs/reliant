// Copyright (c) 2026 Reliant Labs

package assetstore_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
)

// The gtm Service over this store and a real database: an upload is
// presigned to the staging key, confirmed into the asset's own key, and
// downloaded from that key only.
func TestServiceOverAssetStore(t *testing.T) {
	e := newSweepEnv(t)
	ctx := context.Background()
	gate := &featuregate.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, userID, orgID string) (bool, error) {
		return f == featuregate.GTM && userID != "" && orgID == orgA, nil
	}}
	svc := gtm.New(gtm.Deps{Store: e.st, Gate: gate, Assets: assetstore.New(e.mem)})
	person := gtm.Actor{UserID: "user-1", OrgID: orgA, Kind: gtm.ActorPerson}

	up, err := svc.CreateAssetUpload(ctx, person, gtm.CreateAssetUploadInput{
		CampaignID: e.campaigns[orgA], Filename: "logo.png", ContentType: "image/png", SizeBytes: int64(len(pngBytes)),
	})
	require.NoError(t, err)
	staging := "gtm/" + orgA + "/" + up.Asset.ID + ".upload"
	u, err := url.Parse(up.UploadURL)
	require.NoError(t, err)
	assert.Equal(t, "/"+staging, u.Path, "the client writes the staging key only")
	assert.Equal(t, "image/png", up.RequiredHeaders["Content-Type"])

	// The browser's PUT.
	require.NoError(t, e.mem.Put(staging, pngBytes, "image/png"))
	asset, err := svc.ConfirmAssetUpload(ctx, person, gtm.ConfirmAssetUploadInput{AssetID: up.Asset.ID})
	require.NoError(t, err)
	assert.Equal(t, gtm.AssetReady, asset.Status)
	own := "gtm/" + orgA + "/" + up.Asset.ID
	got, ok := e.mem.Get(own)
	require.True(t, ok, "the checked bytes, written server-side to the asset's own key")
	assert.Equal(t, pngBytes, got)
	_, ok = e.mem.Get(staging)
	assert.False(t, ok, "the staging object is gone")

	dl, err := svc.GetAssetDownload(ctx, person, gtm.GetAssetDownloadInput{AssetID: up.Asset.ID})
	require.NoError(t, err)
	d, err := url.Parse(dl.URL)
	require.NoError(t, err)
	assert.Equal(t, "/"+own, d.Path)
	assert.Equal(t, "image/png", d.Query().Get("response-content-type"))
	assert.Equal(t, `inline; filename="logo.png"; filename*=UTF-8''logo.png`, d.Query().Get("response-content-disposition"))

	require.NoError(t, svc.DeleteAsset(ctx, person, gtm.DeleteAssetInput{AssetID: up.Asset.ID}))
	_, ok = e.mem.Get(own)
	assert.False(t, ok)
}
