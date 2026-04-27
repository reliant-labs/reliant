// Copyright (c) 2025 Reliant Labs
package auth

import (
	"testing"
)

func TestNewAPIKeyValidator(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		v, err := NewAPIKeyValidator("rlnt_sk_test-key-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.userID == "" {
			t.Fatal("expected non-empty user ID")
		}
	})

	t.Run("empty key returns error", func(t *testing.T) {
		_, err := NewAPIKeyValidator("")
		if err == nil {
			t.Fatal("expected error for empty key")
		}
	})
}

func TestAPIKeyValidator_ValidateToken(t *testing.T) {
	const key = "rlnt_sk_my-secret-key"
	v, err := NewAPIKeyValidator(key)
	if err != nil {
		t.Fatalf("unexpected error creating validator: %v", err)
	}

	t.Run("correct token", func(t *testing.T) {
		claims, err := v.ValidateToken(key)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.Sub == "" {
			t.Fatal("expected non-empty Sub")
		}
		if claims.Role != "authenticated" {
			t.Errorf("expected role 'authenticated', got %q", claims.Role)
		}
		if claims.Email != "apikey@localhost" {
			t.Errorf("expected email 'apikey@localhost', got %q", claims.Email)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		_, err := v.ValidateToken("wrong-key")
		if err == nil {
			t.Fatal("expected error for wrong key")
		}
	})

	t.Run("empty token", func(t *testing.T) {
		_, err := v.ValidateToken("")
		if err == nil {
			t.Fatal("expected error for empty token")
		}
	})
}

func TestAPIKeyValidator_DeterministicUserID(t *testing.T) {
	const key = "rlnt_sk_deterministic-test"

	v1, _ := NewAPIKeyValidator(key)
	v2, _ := NewAPIKeyValidator(key)

	if v1.userID != v2.userID {
		t.Errorf("expected same user ID for same key, got %q and %q", v1.userID, v2.userID)
	}

	// Different key → different user ID
	v3, _ := NewAPIKeyValidator("rlnt_sk_different-key")
	if v1.userID == v3.userID {
		t.Error("expected different user IDs for different keys")
	}
}

func TestAPIKeyValidator_UserIDFormat(t *testing.T) {
	v, _ := NewAPIKeyValidator("rlnt_sk_format-test")

	// Should look like a UUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	if len(v.userID) != 36 {
		t.Errorf("expected 36-char UUID-like string, got %d chars: %q", len(v.userID), v.userID)
	}

	// Verify dashes at correct positions
	for _, pos := range []int{8, 13, 18, 23} {
		if v.userID[pos] != '-' {
			t.Errorf("expected dash at position %d, got %c in %q", pos, v.userID[pos], v.userID)
		}
	}
}

func TestAPIKeyValidator_ImplementsTokenValidator(t *testing.T) {
	v, _ := NewAPIKeyValidator("rlnt_sk_interface-test")
	var _ TokenValidator = v // compile-time check
}
