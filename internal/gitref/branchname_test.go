// Copyright (c) 2025 Reliant Labs
package gitref

import "testing"

func TestValidateBranchName(t *testing.T) {
	valid := []string{"main", "master", "feature/foo-bar", "release-1.2.3", "user/UPPER_case.v2"}
	for _, name := range valid {
		if err := ValidateBranchName(name); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"main ",                                          // trailing space (callers must trim first)
		"ma in",                                          // interior space
		"-main",                                          // leading dash
		"@",                                              // just "@"
		"a..b",                                           // double dot
		"a/./b",                                          // component starting with dot
		".hidden",                                        // leading dot
		"end.",                                           // trailing dot
		"a.lock",                                         // .lock suffix
		"a//b",                                           // double slash
		"/leading",                                       // leading slash
		"trailing/",                                      // trailing slash
		"a@{b",                                           // reflog syntax
		"a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", // forbidden chars
		"ctrl\x01char", // control char
	}
	for _, name := range invalid {
		if err := ValidateBranchName(name); err == nil {
			t.Errorf("ValidateBranchName(%q) = nil, want error", name)
		}
	}
}

func TestNormalizeBranchName(t *testing.T) {
	cases := map[string]string{
		"main ":    "main",
		" main":    "main",
		"\tmain\n": "main",
		"":         "main",
		"   ":      "main",
		"develop":  "develop",
		"feat/x ":  "feat/x",
	}
	for in, want := range cases {
		if got := NormalizeBranchName(in); got != want {
			t.Errorf("NormalizeBranchName(%q) = %q, want %q", in, got, want)
		}
	}
}
