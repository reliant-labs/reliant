// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateSearchQuery(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		expectWarnings []string // substrings expected in warnings
		expectCount    int
	}{
		{
			name:        "simple query - no warnings",
			query:       "golang context timeout example",
			expectCount: 0,
		},
		{
			name:        "query with OR operator",
			query:       `"PUT" OR "POST" OR "PATCH" email content`,
			expectCount: 2, // OR + excessive quotes
			expectWarnings: []string{
				"boolean operators",
				"quoted phrases",
			},
		},
		{
			name:        "query with AND operator",
			query:       "customer.io AND transactional AND API",
			expectCount: 1,
			expectWarnings: []string{
				"boolean operators",
			},
		},
		{
			name:        "query with site: operator",
			query:       "site:customer.io/docs/api transactional",
			expectCount: 1,
			expectWarnings: []string{
				"site: operator",
			},
		},
		{
			name:        "very long query",
			query:       "Customer.io App API endpoint list campaigns newsletters transactional snippets broadcast actions update content create delete modify CRUD operations REST HTTP methods documentation reference guide",
			expectCount: 1,
			expectWarnings: []string{
				"very long",
			},
		},
		{
			name:        "many quoted phrases",
			query:       `"update_action" OR "update action" OR "campaign_action" OR "newsletter_variant" OR "update_content" email body`,
			expectCount: 2, // OR + many quotes
			expectWarnings: []string{
				"boolean operators",
				"quoted phrases",
			},
		},
		{
			name:        "single quoted phrase - ok",
			query:       `"react hooks" tutorial 2024`,
			expectCount: 0,
		},
		{
			name:        "lowercase or is not a boolean operator",
			query:       "this or that example",
			expectCount: 0,
		},
		{
			name:        "OR in middle of words is not flagged",
			query:       "ORACLE database tutorial",
			expectCount: 0, // "OR" in ORACLE shouldn't match because we use word boundary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := validateSearchQuery(tt.query)
			assert.Len(t, warnings, tt.expectCount, "expected %d warnings for query: %s", tt.expectCount, tt.query)

			for _, expected := range tt.expectWarnings {
				found := false
				for _, w := range warnings {
					if contains(w, expected) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, got warnings: %v", expected, warnings)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestFormatWebSearchResultsZeroResults(t *testing.T) {
	params := WebSearchParams{
		Query: "test query",
		Count: 10,
	}

	result := formatWebSearchResults(nil, params)
	assert.Contains(t, result, "No results found")
	assert.Contains(t, result, "test query")
}
