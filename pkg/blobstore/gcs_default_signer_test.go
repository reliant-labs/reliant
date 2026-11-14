package blobstore_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

func defaultGCS(t *testing.T, credentialJSON string, client *http.Client) *blobstore.GCS {
	t.Helper()
	g, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads", ClientOptions: []option.ClientOption{
		option.WithCredentials(&google.Credentials{JSON: []byte(credentialJSON)}), option.WithHTTPClient(client),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGCSDefaultSignerSelectsCredentialPrincipal(t *testing.T) {
	t.Parallel()
	for _, credentialJSON := range []string{
		`{"type":"service_account","client_email":"` + signerEmail + `"}`,
		`{"type":"external_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/` + signerEmail + `:generateAccessToken"}`,
		`{"type":"impersonated_service_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/` + signerEmail + `:generateAccessToken"}`,
	} {
		calls := 0
		client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if !strings.HasSuffix(r.URL.Path, "/serviceAccounts/"+signerEmail+":signBlob") {
				t.Errorf("wrong default principal: %s", r.URL)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"signedBlob":"3q2+7w=="}`)), Request: r}, nil
		})}
		g := defaultGCS(t, credentialJSON, client)
		req, err := g.PresignDownload(context.Background(), "x", blobstore.DownloadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		q := checkForcedDownload(t, req, "x")
		if calls != 1 || !strings.HasPrefix(q.Get("X-Goog-Credential"), signerEmail+"/") {
			t.Fatalf("calls=%d credential=%q", calls, q.Get("X-Goog-Credential"))
		}
	}
}

func TestGCSDefaultSignerCancelsIAMRequest(t *testing.T) {
	t.Parallel()
	started, exited := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(exited)
		return nil, r.Context().Err()
	})}
	g := defaultGCS(t, `{"type":"service_account","client_email":"`+signerEmail+`"}`, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.PresignDownload(ctx, "x", blobstore.DownloadOptions{}); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("IAM did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("IAM request outlived default signer caller")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled signer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signer did not return")
	}
}

func TestGCSDefaultSignerCancelsMetadataDiscovery(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/email" {
			t.Errorf("unexpected metadata path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		close(started)
		<-r.Context().Done()
		close(exited)
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	g := defaultGCS(t, "", &http.Client{Transport: readerTransport(func(*http.Request) (*http.Response, error) {
		t.Error("IAM reached before discovery")
		return nil, errors.New("unexpected IAM")
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.PresignDownload(ctx, "x", blobstore.DownloadOptions{}); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata did not start")
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("metadata request outlived caller")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("metadata cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not return")
	}
}

func TestGCSDefaultSignerMetadataPrincipal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/email" {
			t.Errorf("unexpected metadata request %s", r.URL.Path)
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = io.WriteString(w, signerEmail)
	}))
	defer srv.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/serviceAccounts/"+signerEmail+":signBlob") {
			t.Errorf("wrong metadata principal %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"signedBlob":"3q2+7w=="}`)), Request: r}, nil
	})}
	req, err := defaultGCS(t, "", client).PresignDownload(context.Background(), "x", blobstore.DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(req.URL)
	if !strings.HasPrefix(u.Query().Get("X-Goog-Credential"), signerEmail+"/") {
		t.Fatalf("wrong signer %s", req.URL)
	}
}

func TestGCSDefaultPrivateKeySignsLocally(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	raw, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": signerEmail, "private_key": string(keyPEM)})
	client := &http.Client{Transport: readerTransport(func(*http.Request) (*http.Response, error) {
		t.Error("local private-key signing made an HTTP call")
		return nil, errors.New("unexpected network")
	})}
	req, err := defaultGCS(t, string(raw), client).PresignDownload(context.Background(), "x", blobstore.DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkForcedDownload(t, req, "x")
}
