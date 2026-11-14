package blobstore_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/blobstore"
	"google.golang.org/api/option"
)

func TestGCSIAMSigningCancelsUnderlyingRequest(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	exited := make(chan struct{})
	client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(exited)
		return nil, r.Context().Err()
	})}
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads", SignerEmail: signerEmail,
		ClientOptions: []option.ClientOption{option.WithHTTPClient(client)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.PresignDownload(ctx, "evil.svg", blobstore.DownloadOptions{}); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("signing never started")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("IAM transport outlived its caller")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("signing cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signing did not return")
	}
}
