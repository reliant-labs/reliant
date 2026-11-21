package devidp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// UserInfo reads the profile claims as well as the address, and
// email_verified in either spelling a provider sends.
func TestUserInfoCarriesTheProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oidc/v1/userinfo" || r.Header.Get("Authorization") != "Bearer at-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sub":"u1","email":"ada@app.test","email_verified":"true","name":"Ada Lovelace",`+
			`"given_name":"Ada","family_name":"Lovelace","preferred_username":"ada","picture":"https://cdn.app.test/u1.png"}`)
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL}
	got, err := c.UserInfo(context.Background(), "at-1")
	if err != nil {
		t.Fatal(err)
	}
	want := UserInfo{Subject: "u1", Email: "ada@app.test", EmailVerified: true, Name: "Ada Lovelace",
		GivenName: "Ada", FamilyName: "Lovelace", PreferredUsername: "ada", Picture: "https://cdn.app.test/u1.png"}
	if got != want {
		t.Errorf("UserInfo = %+v, want %+v", got, want)
	}
	if _, err := c.UserInfo(context.Background(), "at-forged"); err == nil {
		t.Error("a token the issuer rejects returned no error")
	}
}
