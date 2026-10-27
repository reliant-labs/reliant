// Copyright (c) 2025 Reliant Labs
package prhandoff

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublicationRejectsEmptyRelativeAndUnownedPath(t *testing.T) {
	for _, tt := range []struct{ author, claimed string }{{"", "."}, {".", "."}, {"relative", "relative"}, {"/owned/wt", "/different/wt"}} {
		a := Author{OwnerID: "o", RootID: "r", ExecutionID: "e", ChatID: "c", WorktreeID: "w", WorktreePath: tt.author}
		s, _ := New(Deps{Store: &memoryStore{}, Authors: authorsFake{a, true}, GitHub: ghFake{}})
		err := s.Published(context.Background(), PublishInput{Site: Site{RootID: "r", ExecutionID: "e", ChatID: "c"}, WorktreePath: tt.claimed, Branch: "fix", HeadSHA: strings.Repeat("a", 40)})
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("paths %+v accepted: %v", tt, err)
		}
	}
}

func TestReviewMetadataHasNoFindingsAndBoundsIDs(t *testing.T) {
	// Unknown sensitive provider text cannot survive decoding into the persisted
	// metadata contract; only the provider review reference is retained.
	var r Review
	secret := `{"ID":"id","ReviewID":"review","Findings":"secret API key","body":"private source"}`
	if err := json.Unmarshal([]byte(secret), &r); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "Findings") {
		t.Fatalf("retained sensitive text: %s", raw)
	}
	s, _ := New(Deps{Store: &memoryStore{}, Authors: authorsFake{}, GitHub: ghFake{}})
	if _, err := s.Reviewed(context.Background(), ReviewInput{OwnerID: "owner", RepositoryID: 1, Number: 1, ReviewID: strings.Repeat("x", 256)}); !errors.Is(err, ErrRefused) {
		t.Fatalf("oversized reference accepted: %v", err)
	}
}
