package blobstore_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/reliant-labs/forge/pkg/blobstore"
)

type readerTransport func(*http.Request) (*http.Response, error)

func (f readerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An explicit reader must always use IAM signBlob as that reader, even if
// the process credential carries a private key for a different principal.
func TestGCSExplicitReaderUsesIAM(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: readerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/serviceAccounts/"+signerEmail+":signBlob") {
			t.Errorf("unexpected IAM request %s %s", r.Method, r.URL)
		}
		var body struct {
			Payload string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		payload, err := base64.StdEncoding.DecodeString(body.Payload)
		if err != nil || !strings.HasPrefix(string(payload), "GOOG4-RSA-SHA256\n") {
			t.Errorf("bad signing payload %q: %v", payload, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"signedBlob":"3q2+7w=="}`)), Request: r}, nil
	})}
	creds := &google.Credentials{
		JSON:        []byte(`{"type":"service_account","client_email":"writer@example.iam.gserviceaccount.com","private_key":"not-the-reader-key"}`),
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"}),
	}
	g, err := blobstore.NewGCS(ctx, blobstore.GCSConfig{Bucket: "uploads", SignerEmail: signerEmail,
		ClientOptions: []option.ClientOption{option.WithCredentials(creds), option.WithHTTPClient(client)},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := g.PresignDownload(ctx, "evil.svg", blobstore.DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	q := checkForcedDownload(t, req, "evil.svg")
	if calls != 1 || !strings.HasPrefix(q.Get("X-Goog-Credential"), signerEmail+"/") {
		t.Fatalf("calls=%d credential=%q", calls, q.Get("X-Goog-Credential"))
	}
	cancel()
	if _, err := g.PresignDownload(ctx, "x", blobstore.DownloadOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled signing = %v", err)
	}
	if calls != 1 {
		t.Fatal("cancelled call reached IAM")
	}
}

func TestPrivilegedOpaqueKeysDoNotWeakenScoped(t *testing.T) {
	t.Parallel()
	cfg := s3Config("https://s3.example.com")
	cfg.AllowOpaqueKeys = true
	s3, err := blobstore.NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gcs, err := blobstore.NewGCS(context.Background(), blobstore.GCSConfig{Bucket: "uploads", AllowOpaqueKeys: true, SignerEmail: signerEmail,
		SignBytes: (&fakeSigner{}).sign, ClientOptions: []option.ClientOption{option.WithoutAuthentication()},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]blobstore.Store{"s3": s3, "gcs": gcs} {
		t.Run(name, func(t *testing.T) {
			scoped, err := blobstore.Scoped(store, "tenant")
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"/leading", "a//b", "a/../b", "a/./b", "folder/", "back\\slash", "a%2Fb"} {
				req, err := blobstore.PresignDownload(context.Background(), store, key, blobstore.DownloadOptions{DownloadName: "download"})
				if err != nil {
					t.Fatalf("privileged key %q: %v", key, err)
				}
				u, _ := url.Parse(req.URL)
				if u.Path != "/uploads/"+key {
					t.Errorf("key %q became %q", key, u.Path)
				}
				checkForcedDownload(t, req, "download")
				if err := blobstore.ValidateKey(key); err != nil {
					if _, err := blobstore.PresignDownload(context.Background(), scoped, key, blobstore.DownloadOptions{}); !errors.Is(err, blobstore.ErrInvalidKey) {
						t.Errorf("scoped key %q: %v", key, err)
					}
					if _, err := store.PresignPut(context.Background(), key, blobstore.PutOptions{ContentType: "text/plain", ContentLength: 1}); !errors.Is(err, blobstore.ErrInvalidKey) {
						t.Errorf("write key %q: %v", key, err)
					}
					if err := store.Delete(context.Background(), key); !errors.Is(err, blobstore.ErrInvalidKey) {
						t.Errorf("delete key %q: %v", key, err)
					}
				}
			}
		})
	}
}
