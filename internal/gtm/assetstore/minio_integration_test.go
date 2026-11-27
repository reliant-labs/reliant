// Copyright (c) 2026 Reliant Labs

//go:build integration

package assetstore_test

// The AssetStore over a real MinIO, the dev bucket's server: a browser's
// presigned PUT to the staging key (and the signature refusing another
// length), the server's read and write, and what a presigned GET's response
// actually carries — Content-Type, Content-Disposition, and whether the
// backend adds X-Content-Type-Options by itself (logged; S3 and GCS do not).
// It starts the MinIO image forge's own blobstore integration tests pin.
//
//	go test -tags integration -run MinIO ./internal/gtm/assetstore/

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
)

// minioImage is forge's pinned MinIO (pkg/blobstore/s3_integration_test.go).
const minioImage = "cgr.dev/chainguard/minio@sha256:3c60a2a8c4d33409a9bc7a623a7fcbfb4605adc3d89422d2f53b3f673a6e5597"

const (
	minioUser   = "gtm"
	minioSecret = "gtm-integration-secret"
	minioBucket = "gtm-assets"
)

// startMinIO runs MinIO with an empty gtm-assets bucket and returns its
// endpoint.
func startMinIO(t *testing.T) string {
	t.Helper()
	// Not testkit.Slow: it skips in the integration lane, the only one this
	// file builds in.
	if testing.Short() {
		t.Skip("-short: starts a MinIO container")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("no docker on this host: the MinIO test needs a container")
	}
	if exec.Command("docker", "image", "inspect", minioImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", minioImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", minioImage, err, out)
		}
	}
	name := fmt.Sprintf("reliant-gtm-minio-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", "127.0.0.1::9000",
		"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioSecret,
		minioImage, "server", "/data").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "port", name, "9000/tcp").Output()
	require.NoError(t, err)
	hostPort := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	admin, err := minio.New(hostPort, &minio.Options{Creds: credentials.NewStaticV4(minioUser, minioSecret, ""), Region: "us-east-1"})
	require.NoError(t, err)
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = admin.MakeBucket(ctx, minioBucket, minio.MakeBucketOptions{})
		cancel()
		if err == nil {
			return "http://" + hostPort
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("minio never became ready: %v\n%s", err, logs)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// send performs a presigned request, as the browser would.
func send(t *testing.T, method, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestAssetStoreOnMinIO(t *testing.T) {
	endpoint := startMinIO(t)
	blobs, err := assetstore.StoreFromConfig(&configv1.GtmAssetsConfig{
		Endpoint: endpoint, Region: "us-east-1", Bucket: minioBucket,
		AccessKeyId: minioUser, SecretAccessKey: minioSecret, PathStyle: true,
	})
	require.NoError(t, err)
	s := assetstore.New(blobs)
	ctx := context.Background()
	const own = "gtm/org-a/01MINIO"
	staging := own + ".upload"

	put, err := s.PresignPut(ctx, staging, gtm.PutOptions{ContentType: "image/png", Size: int64(len(pngBytes)), TTL: time.Minute})
	require.NoError(t, err)
	res := send(t, http.MethodPut, put.URL, put.Headers, append(append([]byte(nil), pngBytes...), 'x'))
	assert.NotEqual(t, http.StatusOK, res.StatusCode, "the signature binds the length: a longer body is refused")
	res = send(t, http.MethodPut, put.URL, put.Headers, pngBytes)
	require.Equal(t, http.StatusOK, res.StatusCode)

	rc, err := s.Open(ctx, staging)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, pngBytes, got)

	require.NoError(t, s.Put(ctx, own, bytes.NewReader(got), gtm.PutOptions{ContentType: "image/png", Size: int64(len(got))}))
	size, err := s.Stat(ctx, own)
	require.NoError(t, err)
	assert.Equal(t, int64(len(pngBytes)), size)

	inline, err := s.PresignGet(ctx, own, gtm.GetOptions{Inline: true, ContentType: "image/png", Filename: "logo.png"})
	require.NoError(t, err)
	res = send(t, http.MethodGet, inline.URL, nil, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "image/png", res.Header.Get("Content-Type"))
	assert.Equal(t, `inline; filename="logo.png"; filename*=UTF-8''logo.png`, res.Header.Get("Content-Disposition"))
	t.Logf("MinIO X-Content-Type-Options on a presigned GET: %q", res.Header.Get("X-Content-Type-Options"))

	const pdf = "gtm/org-a/01MINIOPDF"
	body := []byte("%PDF-1.7\n")
	require.NoError(t, s.Put(ctx, pdf, bytes.NewReader(body), gtm.PutOptions{ContentType: "application/pdf", Size: int64(len(body))}))
	attach, err := s.PresignGet(ctx, pdf, gtm.GetOptions{Inline: true, ContentType: "application/pdf", Filename: "deck.pdf"})
	require.NoError(t, err)
	res = send(t, http.MethodGet, attach.URL, nil, nil)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "application/pdf", res.Header.Get("Content-Type"), "the stored, server-written type")
	assert.Equal(t, `attachment; filename="deck.pdf"; filename*=UTF-8''deck.pdf`, res.Header.Get("Content-Disposition"))

	for _, key := range []string{staging, own, pdf} {
		require.NoError(t, s.Delete(ctx, key))
		_, err := s.Stat(ctx, key)
		require.ErrorIs(t, err, gtm.ErrObjectNotFound, key)
	}
}
