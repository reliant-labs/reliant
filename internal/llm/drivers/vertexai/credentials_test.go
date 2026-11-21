// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// testServiceAccountKey is the JSON of a service-account key with a fresh RSA
// key, edited by mutate.
func testServiceAccountKey(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	key := map[string]any{
		"type":           "service_account",
		"project_id":     "account-project",
		"private_key_id": "kid-1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "vertex@account-project.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      googleTokenURI,
	}
	if mutate != nil {
		mutate(key)
	}
	b, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseAccountCredential(t *testing.T) {
	t.Run("a service-account key is accepted and signs locally", func(t *testing.T) {
		account, err := parseAccountCredential(testServiceAccountKey(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if account.project != "account-project" {
			t.Fatalf("project = %q, want the key's project_id", account.project)
		}
		tok, err := account.creds.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if strings.Count(tok.Value, ".") != 2 {
			t.Fatalf("token is not a self-signed JWT: %q", tok.Value)
		}
	})

	t.Run("the key's project wins over the server's environment", func(t *testing.T) {
		t.Setenv("GOOGLE_CLOUD_PROJECT", "server-project")
		t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "server-project")
		account, err := parseAccountCredential(testServiceAccountKey(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		for name, get := range map[string]func(context.Context) (string, error){
			"project": account.creds.ProjectID, "quota project": account.creds.QuotaProjectID,
		} {
			if got, _ := get(context.Background()); got != "account-project" {
				t.Errorf("%s = %q, want the key's", name, got)
			}
		}
	})

	for name, raw := range map[string]string{
		"empty":    "",
		"not JSON": "AIzaNotAKey",
		"workload identity": `{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt",` +
			`"token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/var/run/secrets/token"}}`,
		"gcloud login":      `{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`,
		"no project":        testServiceAccountKey(t, func(k map[string]any) { delete(k, "project_id") }),
		"no private key":    testServiceAccountKey(t, func(k map[string]any) { delete(k, "private_key") }),
		"foreign token_uri": testServiceAccountKey(t, func(k map[string]any) { k["token_uri"] = "https://attacker.example/token" }),
		"foreign universe":  testServiceAccountKey(t, func(k map[string]any) { k["universe_domain"] = "attacker.example" }),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			if _, err := parseAccountCredential(raw); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// TestNewClientNeverUsesTheServersGoogleCredentials pins that a Vertex AI
// driver with no connected key is refused, even when the server process has
// Google credentials ADC would find. Drivers run on the multi-tenant server
// roles.
func TestNewClientNeverUsesTheServersGoogleCredentials(t *testing.T) {
	adc := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(adc, []byte(testServiceAccountKey(t, func(k map[string]any) { k["project_id"] = "server-project" })), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "server-project")
	t.Setenv("HOME", t.TempDir())

	for _, id := range []models.ModelID{"vertex-gemini-test", "vertex-claude-test"} {
		if _, err := NewClient(llm.DriverOptions{Model: models.Model{ID: id, APIModel: string(id)}}); !errors.Is(err, ErrNoVertexCredential) {
			t.Errorf("NewClient(%s) with no key: err = %v, want ErrNoVertexCredential", id, err)
		}
	}

	key := testServiceAccountKey(t, nil)
	for _, id := range []models.ModelID{"vertex-gemini-test", "vertex-claude-test"} {
		c, err := NewClient(llm.DriverOptions{ApiKey: key, Model: models.Model{ID: id, APIModel: string(id)}})
		if err != nil {
			t.Fatalf("NewClient(%s) with the account's key: %v", id, err)
		}
		if c.account.project != "account-project" {
			t.Errorf("NewClient(%s) placed in %q, want the key's project", id, c.account.project)
		}
	}
}

// TestConfiguredProjectIsStillTheTarget pins VERTEXAI_PROJECT's meaning: when
// set, it is the project every call targets; unset, the key's project is.
// The account's key signs the call either way.
func TestConfiguredProjectIsStillTheTarget(t *testing.T) {
	t.Cleanup(func() { Configure("", "") })
	key := testServiceAccountKey(t, nil)
	for _, tc := range []struct{ configured, want string }{
		{"", "account-project"},
		{"operator-project", "operator-project"},
	} {
		Configure(tc.configured, "europe-west4")
		for _, id := range []models.ModelID{"vertex-gemini-test", "vertex-claude-test"} {
			c, err := NewClient(llm.DriverOptions{ApiKey: key, Model: models.Model{ID: id, APIModel: string(id)}})
			if err != nil {
				t.Fatalf("NewClient(%s): %v", id, err)
			}
			if got := targetProject(c.account); got != tc.want {
				t.Errorf("VERTEXAI_PROJECT=%q: NewClient(%s) targets %q, want %q", tc.configured, id, got, tc.want)
			}
			if c.provider == providerClaude {
				if ep := c.getClaudeEndpoint(false); !strings.Contains(ep, "/projects/"+tc.want+"/locations/europe-west4/") {
					t.Errorf("VERTEXAI_PROJECT=%q: Claude endpoint %s, want project %s", tc.configured, ep, tc.want)
				}
			}
		}
	}
	if _, err := NewClient(llm.DriverOptions{Model: models.Model{ID: "vertex-claude-test", APIModel: "vertex-claude-test"}}); !errors.Is(err, ErrNoVertexCredential) {
		t.Errorf("a configured project without the account's key: err = %v, want ErrNoVertexCredential", err)
	}
}
