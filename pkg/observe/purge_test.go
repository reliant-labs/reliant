package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/authn"
)

func purgeLine(t *testing.T, ctx context.Context, requestID string, s PurgeSubject) (map[string]any, string, error) {
	t.Helper()
	var buf bytes.Buffer
	id, err := RequestPurge(WithLogger(ctx, slog.New(slog.NewJSONHandler(&buf, nil))), requestID, s)
	if buf.Len() == 0 {
		return nil, id, err
	}
	var m map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &m); jerr != nil {
		t.Fatalf("bad log line %q: %v", buf.String(), jerr)
	}
	return m, id, err
}

func TestRequestPurgeShape(t *testing.T) {
	// A signed-in caller (an admin deleting an account): the request still
	// names no actor, so it is no one's activity.
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "admin-1"})
	m, id, err := purgeLine(t, ctx, "account-42", PurgeSubject{UserID: "u-42", VisitorID: "v-42"})
	if err != nil || id != "account-42" {
		t.Fatalf("RequestPurge = %q, %v", id, err)
	}
	want := map[string]any{
		"level": "INFO", "event.name": "product.forge_purge_requested", "app.client": "backend",
		"product.purge.request_id": "account-42", "product.purge.user_id": "u-42", "product.purge.visitor_id": "v-42",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
	for _, k := range []string{"user.id", "org.id", "visitor.id"} {
		if _, ok := m[k]; ok {
			t.Errorf("the purge request carries an actor %s: %v", k, m)
		}
	}
}

func TestRequestPurgeMintsAnIDAndOmitsAnUnsetSubject(t *testing.T) {
	m, id, err := purgeLine(t, context.Background(), "", PurgeSubject{VisitorID: "install-1"})
	if err != nil || len(id) != 32 || m["product.purge.request_id"] != id {
		t.Fatalf("RequestPurge = %q, %v (line %v)", id, err, m)
	}
	if _, ok := m["product.purge.user_id"]; ok {
		t.Errorf("an unset user id is sent: %v", m)
	}
	_, other, _ := purgeLine(t, context.Background(), "", PurgeSubject{VisitorID: "install-1"})
	if other == id {
		t.Error("two minted request ids are equal")
	}
}

func TestRequestPurgeRefusesWhatItCannotHonour(t *testing.T) {
	for name, c := range map[string]struct {
		id string
		s  PurgeSubject
	}{
		"no subject":       {"", PurgeSubject{}},
		"a truncated id":   {"", PurgeSubject{UserID: strings.Repeat("u", MaxProductPropValueLen+1)}},
		"a bad request id": {"no spaces", PurgeSubject{UserID: "u"}},
		"a quote":          {"x'", PurgeSubject{UserID: "u"}},
	} {
		m, _, err := purgeLine(t, context.Background(), c.id, c.s)
		if !errors.Is(err, ErrInvalidPurge) || m != nil {
			t.Errorf("%s: err = %v, line = %v; want ErrInvalidPurge and nothing sent", name, err, m)
		}
	}
}
