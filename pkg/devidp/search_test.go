package devidp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An issuer answers at most a page of results per search: a revocation must
// read them all, or a planted link or a live session beyond the first page
// survives a revocation that reports success.
func TestRevokeCredentials_RemovesEveryResultBeyondOneIssuerPage(t *testing.T) {
	z := newFakeZitadel(t)
	u := z.addUser("v@acme.com", false, "planted-password")
	z.mu.Lock()
	z.pageLimit = 2
	for i := range 5 {
		u.links = append(u.links, IDPLink{IdPID: fmt.Sprintf("idp-%d", i), UserID: fmt.Sprintf("ext-%d", i)})
		z.sessions[fmt.Sprintf("s-%d", i)] = &fakeSession{token: "t", userID: u.id, verified: []string{"password"}}
	}
	z.mu.Unlock()

	if _, err := z.client().RevokeCredentials(context.Background(), u.id); err != nil {
		t.Fatal(err)
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(u.links) != 0 {
		t.Errorf("%d identity provider links survived the revocation: %v", len(u.links), u.links)
	}
	for id, s := range z.sessions {
		if !s.deleted {
			t.Errorf("session %s survived the revocation", id)
		}
	}
}

func TestSearchAll_FailsClosedOnAnIncompleteAnswer(t *testing.T) {
	for name, answer := range map[string]string{
		"short of the total, unpaged": `{"result":[{"id":"a"}],"details":{"totalResult":"2"}}`,
		"empty page short of total":   `{"result":[],"details":{"totalResult":"3"}}`,
		"results with no total":       `{"result":[{"id":"a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(answer))
			}))
			t.Cleanup(srv.Close)
			c := &Client{Base: srv.URL, Token: "t"}
			if got, err := searchAll[struct{ ID string }](context.Background(), c, "/s", map[string]any{}, "result", false); err == nil {
				t.Fatalf("searchAll = %v, nil: an answer that cannot prove it is complete was accepted", got)
			}
		})
	}
}

func TestSearchAll_EmptyAnswerWithNoTotalIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"details":{}}`))
	}))
	t.Cleanup(srv.Close)
	got, err := searchAll[struct{ ID string }](context.Background(), &Client{Base: srv.URL, Token: "t"}, "/s", map[string]any{}, "result", true)
	if err != nil || len(got) != 0 {
		t.Fatalf("searchAll = %v, %v; want an empty result", got, err)
	}
}
