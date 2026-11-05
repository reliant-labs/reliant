// Copyright (c) 2026 Reliant Labs

//go:build integration

package blobstore_test

// The S3 backend against a real MinIO: an upload through a presigned PUT,
// the size and type the signature binds, the public-endpoint override,
// Stat/List/Delete and the Scoped tenant boundary. Gated by the
// `integration` tag and by -short.
//
//	go test -tags integration -run S3 ./pkg/blobstore/

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

// minioImage is the MinIO the dev compose stack runs (forge's scaffolded
// docker-compose.yml). Upstream stopped publishing images in 2025; Chainguard
// builds it from source.
const minioImage = "cgr.dev/chainguard/minio@sha256:3c60a2a8c4d33409a9bc7a623a7fcbfb4605adc3d89422d2f53b3f673a6e5597"

const (
	minioUser   = "forge"
	minioSecret = "forge-integration-secret"
	bucket      = "uploads"
)

// startMinIO runs MinIO with one empty bucket and returns its port.
func startMinIO(t *testing.T) int {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: starts a MinIO container")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	if exec.Command("docker", "image", "inspect", minioImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", minioImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", minioImage, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	name := fmt.Sprintf("blobstore-minio-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioSecret,
		minioImage, "server", "/data").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	admin, err := minio.New(fmt.Sprintf("127.0.0.1:%d", port), &minio.Options{
		Creds:  credentials.NewStaticV4(minioUser, minioSecret, ""),
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
		cancel()
		if err == nil {
			return port
		}
		if time.Now().After(deadline) {
			out, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("minio never became ready: %v\n%s", err, out)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// send performs a presigned request with body, the way a browser would:
// the presigned headers, and a Content-Length taken from the body.
func send(t *testing.T, req blobstore.PresignedRequest, body []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	hr, err := http.NewRequest(req.Method, req.URL, r)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestS3AgainstMinIO(t *testing.T) {
	port := startMinIO(t)
	ctx := context.Background()
	// The API dials 127.0.0.1; the "browser" uses localhost: the presigned
	// URLs must name the public host, and the signature must hold there.
	s, err := blobstore.NewS3(blobstore.S3Config{
		Endpoint:        fmt.Sprintf("http://127.0.0.1:%d", port),
		PublicEndpoint:  fmt.Sprintf("http://localhost:%d", port),
		Bucket:          bucket,
		AccessKeyID:     minioUser,
		SecretAccessKey: minioSecret,
		PathStyle:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("\x89PNG not really, but ten+ bytes")

	put := func(key string, ct string, n int64) blobstore.PresignedRequest {
		t.Helper()
		req, err := s.PresignPut(ctx, key, blobstore.PutOptions{ContentType: ct, ContentLength: n, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(req.URL, fmt.Sprintf("http://localhost:%d/", port)) {
			t.Fatalf("presigned URL %q is not on the public endpoint", req.URL)
		}
		return req
	}

	t.Run("the signature binds the size", func(t *testing.T) {
		req := put("t1/short.png", "image/png", int64(len(body)))
		if code, out := send(t, req, append(body, 'x')); code != http.StatusForbidden {
			t.Fatalf("a longer body than presigned: %d %s, want 403", code, out)
		}
		if _, err := s.Stat(ctx, "t1/short.png"); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("a refused upload left an object: %v", err)
		}
	})

	t.Run("the signature binds the type", func(t *testing.T) {
		req := put("t1/typed.png", "image/png", int64(len(body)))
		req.Headers["Content-Type"] = "text/html"
		if code, out := send(t, req, body); code != http.StatusForbidden {
			t.Fatalf("a different Content-Type than presigned: %d %s, want 403", code, out)
		}
	})

	t.Run("upload, stat, download, list, delete", func(t *testing.T) {
		if code, out := send(t, put("t1/a.png", "image/png", int64(len(body))), body); code != http.StatusOK {
			t.Fatalf("upload: %d %s", code, out)
		}
		if code, out := send(t, put("t1/sub/b.png", "image/png", 3), []byte("abc")); code != http.StatusOK {
			t.Fatalf("upload: %d %s", code, out)
		}
		if code, out := send(t, put("t2/c.png", "image/png", 3), []byte("xyz")); code != http.StatusOK {
			t.Fatalf("upload: %d %s", code, out)
		}
		o, err := s.Stat(ctx, "t1/a.png")
		if err != nil || o.Size != int64(len(body)) || o.ContentType != "image/png" || o.ETag == "" {
			t.Fatalf("Stat = %+v, %v", o, err)
		}
		get, err := s.PresignGet(ctx, "t1/a.png", blobstore.GetOptions{DownloadName: "a.png"})
		if err != nil {
			t.Fatal(err)
		}
		code, got := send(t, get, nil)
		if code != http.StatusOK || !bytes.Equal(got, body) {
			t.Fatalf("download: %d %q", code, got)
		}

		page, err := s.List(ctx, "t1/", blobstore.ListOptions{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(keys(page), ",") != "t1/a.png" || page.Next != "t1/a.png" {
			t.Fatalf("first page = %v next %q", keys(page), page.Next)
		}
		page, err = s.List(ctx, "t1/", blobstore.ListOptions{Limit: 1, After: page.Next})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(keys(page), ",") != "t1/sub/b.png" || page.Next != "" {
			t.Fatalf("second page = %v next %q", keys(page), page.Next)
		}

		scoped, err := blobstore.Scoped(s, "t2")
		if err != nil {
			t.Fatal(err)
		}
		page, err = scoped.List(ctx, "", blobstore.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(keys(page), ",") != "c.png" {
			t.Fatalf("scoped list = %v, want only t2's object without its prefix", keys(page))
		}
		if _, err := scoped.Stat(ctx, "../t1/a.png"); !errors.Is(err, blobstore.ErrInvalidKey) {
			t.Fatalf("scoped Stat escaped its prefix: %v", err)
		}

		if err := s.Delete(ctx, "t1/a.png"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Stat(ctx, "t1/a.png"); !errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Stat after Delete = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "t1/a.png"); err != nil {
			t.Fatalf("deleting a missing object = %v, want nil", err)
		}
	})

	t.Run("a wrong credential is a failure, not not-found", func(t *testing.T) {
		bad, err := blobstore.NewS3(blobstore.S3Config{
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d", port), Bucket: bucket,
			AccessKeyID: minioUser, SecretAccessKey: "wrong", PathStyle: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bad.Stat(ctx, "t2/c.png"); err == nil || errors.Is(err, blobstore.ErrNotFound) {
			t.Fatalf("Stat with a wrong secret = %v", err)
		}
	})
}
