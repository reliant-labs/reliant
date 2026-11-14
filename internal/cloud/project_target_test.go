package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHumanRequestResolvesServerProjectBeforeEffects(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "member", true: "removed"}[refuse], func(t *testing.T) {
			calls := []string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.Header.Get("Authorization") != "Bearer one-human" {
					t.Fatal("identity changed with target")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/controlplane.v1.DeployService/ResolveHostedProject" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if !reflect.DeepEqual(body, map[string]any{"name": "shop"}) {
						t.Errorf("request guessed org: %v", body)
					}
					if refuse {
						w.WriteHeader(403)
						_, _ = w.Write([]byte(`{"code":"permission_denied","message":"membership removed"}`))
						return
					}
					_, _ = w.Write([]byte(`{"projectId":"server-project","orgId":"A"}`))
					return
				}
				if r.Header.Get("Forge-Project-ID") != "server-project" {
					t.Error("effect has no server project identity")
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			client := NewClient(Endpoint{URL: srv.URL, ProjectName: "shop"}, Credential{Kind: "human", Token: "one-human"})
			err := client.Call(context.Background(), "controlplane.v1.DeployService/EnsureEnvironment", map[string]any{}, nil)
			if refuse {
				if err == nil || len(calls) != 1 {
					t.Fatalf("refused membership reached effect: %v %v", calls, err)
				}
			} else if err != nil || len(calls) != 2 {
				t.Fatalf("member request: %v %v", calls, err)
			}
		})
	}
}
