// Copyright (c) 2026 Reliant Labs

package assetstore_test

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
)

const (
	finalKey   = "gtm/org-a/01ASSET"
	stagingKey = "gtm/org-a/01ASSET.upload"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func newStore(t *testing.T) (gtm.AssetStore, *blobstore.Memory) {
	t.Helper()
	mem := blobstore.NewMemory()
	s := assetstore.New(mem)
	require.NotNil(t, s)
	return s, mem
}

// query is the presigned URL's query.
func query(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Query()
}

func TestNewWithoutABucketIsOff(t *testing.T) {
	assert.Nil(t, assetstore.New(nil), "no bucket: a nil AssetStore, so the Service fails closed")
}

func TestKeys(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, key := range []string{
		"", "gtm", "gtm/", "gtm/org-a", "gtm/org-a/", "gtm//id", "other/org-a/id",
		"gtm/org-a/id/extra", "gtm/org-a/.upload", "/gtm/org-a/id", "gtm/org-a/../org-b/id",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := s.Stat(ctx, key)
			require.ErrorIs(t, err, blobstore.ErrInvalidKey)
			require.ErrorIs(t, s.Delete(ctx, key), blobstore.ErrInvalidKey)
			_, err = s.Open(ctx, key)
			require.ErrorIs(t, err, blobstore.ErrInvalidKey)
		})
	}
}

func TestPresignPut(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	t.Run("a staging key, signed for the exact length and type, within the TTL", func(t *testing.T) {
		before := time.Now()
		req, err := s.PresignPut(ctx, stagingKey, gtm.PutOptions{ContentType: "image/png", Size: 1234, TTL: 10 * time.Minute})
		require.NoError(t, err)
		after := time.Now()
		assert.Equal(t, map[string]string{"Content-Type": "image/png"}, req.Headers)
		assert.Equal(t, "1234", query(t, req.URL).Get("content-length"))
		assert.Contains(t, req.URL, "/"+stagingKey)
		assert.False(t, req.ExpiresAt.Before(before.Add(10*time.Minute).Truncate(time.Second)))
		assert.False(t, req.ExpiresAt.After(after.Add(10*time.Minute)))
	})
	t.Run("never an asset's own key", func(t *testing.T) {
		_, err := s.PresignPut(ctx, finalKey, gtm.PutOptions{ContentType: "image/png", Size: 1, TTL: time.Minute})
		require.ErrorIs(t, err, blobstore.ErrInvalidKey)
	})
	t.Run("a TTL is required", func(t *testing.T) {
		_, err := s.PresignPut(ctx, stagingKey, gtm.PutOptions{ContentType: "image/png", Size: 1})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions)
	})
	t.Run("a size and a type are required", func(t *testing.T) {
		_, err := s.PresignPut(ctx, stagingKey, gtm.PutOptions{ContentType: "image/png", TTL: time.Minute})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions)
		_, err = s.PresignPut(ctx, stagingKey, gtm.PutOptions{Size: 1, TTL: time.Minute})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions)
	})
	t.Run("nothing over the table's 64 MiB ceiling", func(t *testing.T) {
		_, err := s.PresignPut(ctx, stagingKey, gtm.PutOptions{ContentType: "image/png", Size: gtm.MaxCopiedAssetBytes + 1, TTL: time.Minute})
		require.ErrorIs(t, err, blobstore.ErrTooLarge)
		_, err = s.PresignPut(ctx, stagingKey, gtm.PutOptions{ContentType: "image/png", Size: gtm.MaxCopiedAssetBytes, TTL: time.Minute})
		require.NoError(t, err)
	})
}

func TestOpen(t *testing.T) {
	s, mem := newStore(t)
	ctx := context.Background()

	_, err := s.Open(ctx, stagingKey)
	require.ErrorIs(t, err, gtm.ErrObjectNotFound, "not uploaded yet")

	require.NoError(t, mem.Put(stagingKey, pngBytes, "image/png"))
	rc, err := s.Open(ctx, stagingKey)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, pngBytes, got, "the staging object, whole")
}

func TestPut(t *testing.T) {
	ctx := context.Background()

	t.Run("an asset's own key, the bytes and type given", func(t *testing.T) {
		s, mem := newStore(t)
		require.NoError(t, s.Put(ctx, finalKey, bytes.NewReader(pngBytes), gtm.PutOptions{ContentType: "image/png", Size: int64(len(pngBytes))}))
		got, ok := mem.Get(finalKey)
		require.True(t, ok)
		assert.Equal(t, pngBytes, got)
		obj, err := mem.Stat(ctx, finalKey)
		require.NoError(t, err)
		assert.Equal(t, "image/png", obj.ContentType)
	})
	t.Run("never a staging key", func(t *testing.T) {
		s, mem := newStore(t)
		err := s.Put(ctx, stagingKey, bytes.NewReader(pngBytes), gtm.PutOptions{ContentType: "image/png", Size: int64(len(pngBytes))})
		require.ErrorIs(t, err, blobstore.ErrInvalidKey)
		_, ok := mem.Get(stagingKey)
		assert.False(t, ok)
	})
	t.Run("a body of another length stores nothing and keeps what was there", func(t *testing.T) {
		s, mem := newStore(t)
		require.NoError(t, mem.Put(finalKey, []byte("before"), "text/plain"))
		err := s.Put(ctx, finalKey, bytes.NewReader(pngBytes), gtm.PutOptions{ContentType: "image/png", Size: int64(len(pngBytes)) - 1})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions, "longer than declared")
		err = s.Put(ctx, finalKey, bytes.NewReader(pngBytes), gtm.PutOptions{ContentType: "image/png", Size: int64(len(pngBytes)) + 1})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions, "shorter than declared")
		got, _ := mem.Get(finalKey)
		assert.Equal(t, []byte("before"), got)
	})
	t.Run("the size is required, and at most 64 MiB, refused before reading", func(t *testing.T) {
		s, _ := newStore(t)
		err := s.Put(ctx, finalKey, bytes.NewReader(pngBytes), gtm.PutOptions{ContentType: "image/png"})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions)
		err = s.Put(ctx, finalKey, iotest.ErrReader(io.ErrUnexpectedEOF), gtm.PutOptions{ContentType: "image/png", Size: gtm.MaxCopiedAssetBytes + 1})
		require.ErrorIs(t, err, blobstore.ErrTooLarge)
	})
}

func TestStatAndDelete(t *testing.T) {
	s, mem := newStore(t)
	ctx := context.Background()

	_, err := s.Stat(ctx, finalKey)
	require.ErrorIs(t, err, gtm.ErrObjectNotFound)
	require.NoError(t, s.Delete(ctx, finalKey), "a missing object is not an error")

	require.NoError(t, mem.Put(finalKey, pngBytes, "image/png"))
	require.NoError(t, mem.Put(stagingKey, pngBytes, "image/png"))
	size, err := s.Stat(ctx, finalKey)
	require.NoError(t, err)
	assert.Equal(t, int64(len(pngBytes)), size)

	require.NoError(t, s.Delete(ctx, stagingKey))
	_, ok := mem.Get(stagingKey)
	assert.False(t, ok)
	_, ok = mem.Get(finalKey)
	assert.True(t, ok, "only the key named")
	require.NoError(t, s.Delete(ctx, finalKey))
	_, err = s.Stat(ctx, finalKey)
	require.ErrorIs(t, err, gtm.ErrObjectNotFound)
}

func TestPresignGet(t *testing.T) {
	ctx := context.Background()
	s, mem := newStore(t)
	require.NoError(t, mem.Put(finalKey, pngBytes, "image/png"))
	pdfKey := "gtm/org-a/01PDF"
	require.NoError(t, mem.Put(pdfKey, []byte("%PDF-1.7"), "application/pdf"))

	t.Run("inline for a safe type, the type forced on the response", func(t *testing.T) {
		before := time.Now()
		req, err := s.PresignGet(ctx, finalKey, gtm.GetOptions{Inline: true, ContentType: "image/png", Filename: "logo.png"})
		require.NoError(t, err)
		q := query(t, req.URL)
		assert.Equal(t, "image/png", q.Get("response-content-type"))
		assert.Equal(t, `inline; filename="logo.png"; filename*=UTF-8''logo.png`, q.Get("response-content-disposition"))
		assert.False(t, req.ExpiresAt.After(time.Now().Add(assetstore.DownloadTTL)))
		assert.False(t, req.ExpiresAt.Before(before.Add(assetstore.DownloadTTL).Truncate(time.Second)))
	})
	t.Run("a type that is not InlineSafe is an attachment, even when inline is asked", func(t *testing.T) {
		req, err := s.PresignGet(ctx, pdfKey, gtm.GetOptions{Inline: true, ContentType: "application/pdf", Filename: "deck.pdf"})
		require.NoError(t, err)
		q := query(t, req.URL)
		assert.Empty(t, q.Get("response-content-type"), "the stored type, which only Put wrote")
		assert.Equal(t, `attachment; filename="deck.pdf"; filename*=UTF-8''deck.pdf`, q.Get("response-content-disposition"))
	})
	t.Run("not inline: an attachment, for a safe type too", func(t *testing.T) {
		req, err := s.PresignGet(ctx, finalKey, gtm.GetOptions{ContentType: "image/png", Filename: "logo.png"})
		require.NoError(t, err)
		q := query(t, req.URL)
		assert.True(t, strings.HasPrefix(q.Get("response-content-disposition"), "attachment;"))
	})
	t.Run("a stored type that is not the one asked for downloads as octet-stream", func(t *testing.T) {
		key := "gtm/org-a/01DRIFT"
		require.NoError(t, mem.Put(key, []byte("<html>"), "text/html"))
		req, err := s.PresignGet(ctx, key, gtm.GetOptions{ContentType: "text/plain", Filename: "notes.txt"})
		require.NoError(t, err)
		q := query(t, req.URL)
		assert.Equal(t, blobstore.DownloadContentType, q.Get("response-content-type"))
		assert.True(t, strings.HasPrefix(q.Get("response-content-disposition"), "attachment;"))
	})
	t.Run("the filename per RFC 6266: an ASCII fallback, the real name in filename*", func(t *testing.T) {
		req, err := s.PresignGet(ctx, finalKey, gtm.GetOptions{Inline: true, ContentType: "image/png", Filename: "résumé \"x\".png"})
		require.NoError(t, err)
		assert.Equal(t, `inline; filename="r_sum_ _x_.png"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%22x%22.png`,
			query(t, req.URL).Get("response-content-disposition"))
	})
	t.Run("no filename names it file", func(t *testing.T) {
		req, err := s.PresignGet(ctx, finalKey, gtm.GetOptions{Inline: true, ContentType: "image/png"})
		require.NoError(t, err)
		assert.Equal(t, `inline; filename="file"; filename*=UTF-8''file`, query(t, req.URL).Get("response-content-disposition"))
	})
	t.Run("a missing object, for an attachment, is ErrObjectNotFound", func(t *testing.T) {
		_, err := s.PresignGet(ctx, "gtm/org-a/01GONE", gtm.GetOptions{ContentType: "application/pdf", Filename: "x.pdf"})
		require.ErrorIs(t, err, gtm.ErrObjectNotFound)
	})
	t.Run("never a staging key: its bytes are unchecked", func(t *testing.T) {
		require.NoError(t, mem.Put(stagingKey, pngBytes, "image/png"))
		_, err := s.PresignGet(ctx, stagingKey, gtm.GetOptions{Inline: true, ContentType: "image/png", Filename: "x.png"})
		require.ErrorIs(t, err, blobstore.ErrInvalidKey)
	})
	t.Run("a content type is required", func(t *testing.T) {
		_, err := s.PresignGet(ctx, finalKey, gtm.GetOptions{Filename: "x.png"})
		require.ErrorIs(t, err, blobstore.ErrInvalidOptions)
	})
}
