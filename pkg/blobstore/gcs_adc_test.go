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
	"google.golang.org/api/option"
)

// Exercise real ADC resolution, not injected credential JSON. Isolate every
// credential source and metadata endpoint so no external host is contacted.
func TestGCSDefaultADCMetadataCancellation(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	started, exited := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/email" {
			t.Errorf("unexpected background discovery %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		close(started)
		<-r.Context().Done()
		close(exited)
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads", ClientOptions: []option.ClientOption{option.WithHTTPClient(&http.Client{Transport: readerTransport(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected IAM request")
		return nil, errors.New("unexpected IAM")
	})})}})
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
		t.Fatal("ADC metadata did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("ADC metadata HTTP outlived caller")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ADC cancellation %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ADC signer did not return")
	}
}

func TestGCSDefaultADCMetadataPrincipal(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/email" {
			t.Errorf("unexpected ADC request %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = io.WriteString(w, signerEmail)
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	calls := 0
	client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/serviceAccounts/"+signerEmail+":signBlob") {
			t.Errorf("wrong ADC principal %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"signedBlob":"3q2+7w=="}`)), Request: r}, nil
	})}
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads", ClientOptions: []option.ClientOption{option.WithHTTPClient(client)}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := g.PresignDownload(context.Background(), "x", blobstore.DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := checkForcedDownload(t, req, "x")
	if calls != 1 || !strings.HasPrefix(q.Get("X-Goog-Credential"), signerEmail+"/") {
		t.Fatalf("wrong default ADC signing calls=%d query=%v", calls, q)
	}
}
