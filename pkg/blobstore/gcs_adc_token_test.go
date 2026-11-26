package blobstore_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

func TestGCSDefaultADCCancelsCredentialTokenHTTP(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	started, exited := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Metadata-Flavor", "Google")
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/service-accounts/default/email":
			_, _ = io.WriteString(w, signerEmail)
		case "/computeMetadata/v1/universe/universe-domain":
			_, _ = io.WriteString(w, "googleapis.com")
		case "/computeMetadata/v1/instance/service-accounts/default/token":
			close(started)
			<-r.Context().Done()
			close(exited)
		default:
			t.Errorf("unexpected credential request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	// No HTTP/auth client injection: the actual default token provider must carry
	// the presign context into its metadata HTTP, before IAM can be contacted.
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.PresignDownload(ctx, "x", blobstore.DownloadOptions{}); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("ADC token HTTP did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("credential token HTTP outlived caller")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("token cancellation %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signer did not return")
	}
}
