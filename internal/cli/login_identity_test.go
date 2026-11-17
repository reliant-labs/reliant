package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// `forge login --token` reports the stored credential by its display prefix
// and never echoes the token it was handed.
func TestLoginOne_ReportsThePrefixNotTheToken(t *testing.T) {
	const secret = "rlat_EXAMPLE2NOTAREALTOKENxxxxxxxxxx"
	ep, err := cloud.ResolveEndpoint("prod", &cloud.Declaration{Endpoint: "https://cp.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := loginOne(context.Background(), declaredControlPlane{Endpoint: ep, Envs: []string{"prod"}}, secret, true, path, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, secret) || strings.Contains(got, "NOTAREALTOKEN") {
		t.Fatalf("login output echoes the token:\n%s", got)
	}
	if !strings.Contains(got, "rlat_EXAMPLE2…") {
		t.Fatalf("login output does not name the credential by prefix:\n%s", got)
	}
}
