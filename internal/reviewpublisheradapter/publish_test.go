package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/stretchr/testify/require"
)

type internalAuth struct{}

func (internalAuth) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer private-service-secret")
	return nil
}

func TestBridgeFixedOperationWireAndBoundReceipt(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(fmt.Sprint(reconcile), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := "PublishReviewCheck"
				if reconcile {
					method = "ReconcileReviewCheck"
				}
				require.Equal(t, bridgeService+method, r.URL.Path)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "Bearer private-service-secret", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "owner", body["userId"])
				require.Equal(t, "42", body["repositoryId"])
				require.Equal(t, "binding-external", body["externalId"])
				require.Equal(t, "ReliantReview", body["name"])
				if reconcile {
					require.NotContains(t, body, "summary")
					require.NotContains(t, body, "conclusion")
				} else {
					require.Equal(t, "REVIEW_CHECK_STATUS_COMPLETED", body["status"])
					require.Equal(t, "REVIEW_CHECK_CONCLUSION_SUCCESS", body["conclusion"])
				}
				fmt.Fprint(w, `{"checkId":"901","checkUrl":"https://github.com/acme/repo/runs/901","state":"REVIEW_CHECK_RECONCILE_STATE_FOUND"}`)
			}))
			defer srv.Close()
			b := BridgeHTTP{Origin: srv.URL, Client: srv.Client(), Auth: internalAuth{}}
			in := CheckRequest{OwnerID: "owner", RepositoryID: 42, Owner: "acme", Repo: "repo", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "ReliantReview", ExternalID: "binding-external", RunURL: "https://app.example.test/workflows/runs/root", Summary: "approved", Conclusion: "success"}
			var got CheckReceipt
			var err error
			if reconcile {
				got, err = b.Reconcile(context.Background(), in)
			} else {
				got, err = b.Publish(context.Background(), in)
			}
			require.NoError(t, err)
			require.Equal(t, int64(901), got.ID)
		})
	}
}

func TestBridgeUnknownNeverBecomesSuccessAndErrorsRedacted(t *testing.T) {
	for _, body := range []string{
		`{"checkId":"901","checkUrl":"https://github.com/acme/repo/runs/901","state":"REVIEW_CHECK_RECONCILE_STATE_NOT_FOUND"}`,
		`{"state":"REVIEW_CHECK_RECONCILE_STATE_UNKNOWN"}`,
		`{"checkId":"0","checkUrl":"https://github.com/acme/repo/runs/901","state":"REVIEW_CHECK_RECONCILE_STATE_FOUND"}`,
		`{"checkId":"901","checkUrl":"https://evil.test/secret","state":"REVIEW_CHECK_RECONCILE_STATE_FOUND"}`,
		`private-service-secret`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			b := BridgeHTTP{Origin: srv.URL, Client: srv.Client(), Auth: internalAuth{}}
			got, err := b.Reconcile(context.Background(), CheckRequest{})
			require.ErrorIs(t, err, reviewpublisher.ErrUnavailable)
			require.Zero(t, got.ID)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestBridgeRejectsAllRedirects(t *testing.T) {
	var redirected int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; t.Error("internal auth redirected") }))
	defer target.Close()
	for _, status := range []int{303, 307} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
		b := BridgeHTTP{Origin: srv.URL, Client: srv.Client(), Auth: internalAuth{}}
		_, err := b.Reconcile(context.Background(), CheckRequest{})
		require.ErrorIs(t, err, reviewpublisher.ErrUnavailable)
		srv.Close()
	}
	require.Zero(t, redirected)
}
