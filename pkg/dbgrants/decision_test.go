package dbgrants

import (
	"strings"
	"testing"
)

// The names below are the credential columns of the two production schemas that
// motivated the fail-closed default (control-plane and reliant), plus the
// generic spellings. Every one must be judged credential-shaped.
func TestCredentialShapedCatchesCredentialColumns(t *testing.T) {
	for _, name := range []string{
		"credential_token", "access_token", "refresh_token", "session_token", "token", "api_token", "id_token",
		"litellm_key_plaintext_encrypted", "secret_sealed", "access_token_sealed", "claude_token_sealed", "ciphertext",
		"api_key", "apikey", "ApiKey", "x_api_key", "private_key", "signing_key", "encryption_key", "secret_key", "access_key",
		"password", "password_hash", "passwd", "passphrase", "pwd",
		"token_hash", "key_hash", "api_key_hash", "secret", "client_secret", "webhook_secret", "credential", "credentials", "creds",
		"bearer", "bearer_token", "jwt", "otp", "totp_secret", "recovery_code", "backup_codes_hashed", "salt", "password_salt",
		"session_cookie", "cookie", "cipher", "pin", "pin_hash", "hash", "hashed",
	} {
		if !CredentialShaped(name) {
			t.Errorf("CredentialShaped(%q) = false, want true", name)
		}
	}
}

// What must NOT trip: names that merely contain a stem inside another word, or
// describe a credential rather than hold one. A noisy list is a list authors
// learn to blanket-declare readable.
func TestCredentialShapedLeavesOrdinaryColumnsAlone(t *testing.T) {
	for _, name := range []string{
		"id", "email", "name", "created_at", "updated_at", "deleted_at", "status", "body", "title", "owner_id",
		"pinned", "pinned_at", "spinner", "basalt", "tokenizer_name", "monkey", "turkey", "keyword", "keywords", "key_name",
		"hashtag", "cashier", "salty", "passenger", "passage", "cooking",
		"token_id", "token_name", "token_type", "token_status", "token_count", "secret_name", "secret_id", "secret_version",
		"credential_id", "credential_type", "credential_provider", "api_key_id", "api_key_name", "api_key_status",
		"password_policy_id", "hash_algorithm_id",
	} {
		if CredentialShaped(name) {
			t.Errorf("CredentialShaped(%q) = true, want false", name)
		}
	}
}

func TestDispositionCombinesTheDeclarations(t *testing.T) {
	cases := []struct {
		name string
		col  Column
		want Disposition
	}{
		{"plain column is granted", Column{Name: "email"}, Granted},
		{"undecided credential is withheld", Column{Name: "access_token"}, WithheldUndecided},
		{"declared secret is withheld", Column{Name: "access_token", Secret: true}, WithheldSecret},
		{"a secret with an innocent name is withheld", Column{Name: "value", Secret: true}, WithheldSecret},
		{"readable grants a credential-shaped name", Column{Name: "token_hash", Readable: true}, Granted},
		{"secret wins over readable", Column{Name: "token_hash", Secret: true, Readable: true}, WithheldSecret},
		{"a numeric counter named for tokens is not a credential", Column{Name: "input_tokens", Kind: KindScalar}, Granted},
		{"a timestamp named for a token is not a credential", Column{Name: "token_expires_at", Kind: KindScalar}, Granted},
		{"bytea is opaque and needs a decision whatever it is called", Column{Name: "payload", Kind: KindBinary}, WithheldUndecided},
		{"bytea can be declared readable", Column{Name: "payload", Kind: KindBinary, Readable: true}, Granted},
		{"unset kind is the strict reading", Column{Name: "api_key", Kind: KindText}, WithheldUndecided},
	}
	for _, tc := range cases {
		if got := tc.col.Disposition(); got != tc.want {
			t.Errorf("%s: Disposition = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Render never lists an undecided column as readable, and says why it left it
// out, with both ways to decide.
func TestRenderWithholdsUndecidedColumns(t *testing.T) {
	script, err := Render([]Table{{Schema: "public", Name: "git_credentials", Columns: []Column{
		{Name: "id"}, {Name: "provider"}, {Name: "access_token"}, {Name: "refresh_token", Secret: true},
		{Name: "token_hash", Readable: true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "ARRAY['id', 'provider', 'token_hash']::text[]") {
		t.Errorf("the readable set is not exactly id, provider, token_hash:\n%s", script)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if strings.Contains(line, "access_token") || strings.Contains(line, "refresh_token") {
			t.Errorf("a withheld column outside a comment: %q", line)
		}
	}
	for _, want := range []string{
		"--   public.git_credentials: access_token",
		"credential-shaped columns nobody decided",
		"forge:secret",
		"forge:readable",
		"--   public.git_credentials: refresh_token",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}

func TestUndecidedListsOnlyUndecidedColumnsSorted(t *testing.T) {
	got := Undecided([]Table{
		{Schema: "public", Name: "b", Columns: []Column{{Name: "x_token"}, {Name: "ok"}}},
		{Schema: "public", Name: "a", Columns: []Column{{Name: "password"}, {Name: "secret", Secret: true}, {Name: "token_hash", Readable: true}}},
	})
	var names []string
	for _, f := range got {
		names = append(names, f.String())
	}
	if want := "public.a.password,public.b.x_token"; strings.Join(names, ",") != want {
		t.Errorf("Undecided = %v, want %s", names, want)
	}
}
