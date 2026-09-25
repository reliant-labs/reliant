// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// TestDaemonStartToken_RefusesNonAccessTokens pins the CLI's format check:
// `reliant daemon start --token` accepts only an `rlat_` access token.
//
// Mutation caught: dropping (or loosening) the accesstoken.HasFormat check,
// which would write a retired-family string into daemon.json.
func TestDaemonStartToken_RefusesNonAccessTokens(t *testing.T) {
	withTempReliantHome(t)
	minted, err := fat.Mint()
	if err != nil {
		t.Fatal(err)
	}
	conn := &connection{ServerURL: "https://api.example.test"}

	for _, tc := range []struct {
		input string
		ok    bool
	}{
		{"rlnt_pat_legacy0000000000000000000000000", false},
		{"rlat_short", false},
		{minted.Plaintext, true},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString(tc.input + "\n")
		_ = w.Close()
		orig := os.Stdin
		os.Stdin = r
		cmd := NewRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		creds, err := credentialsFromToken(context.Background(), cmd, conn, "")
		os.Stdin = orig
		_ = r.Close()

		if tc.ok {
			if err != nil || creds == nil || creds.PAT != tc.input {
				t.Fatalf("%q: want accepted, got creds=%v err=%v", tc.input, creds, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "invalid token format") {
			t.Fatalf("%q: want invalid token format, got creds=%v err=%v", tc.input, creds, err)
		}
	}
}
