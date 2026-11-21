// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

func TestDiffRecords(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	long := timestamppb.New(now.Add(-time.Hour))
	recs := []*reliantv1.PullRequestRecord{
		{Number: 1, HeadSha: "a", State: "open", Labels: []string{"review:passed"}, ObservedAt: long,
			Verdict: &reliantv1.PullRequestVerdict{Kind: "passed"},
			Checks:  []*reliantv1.PullRequestCheck{{Name: "Reliant review", CheckRunId: 3, Conclusion: "success", CompletedAt: long}}},
		{Number: 2, HeadSha: "b", State: "open", Labels: []string{"review:passed"}, ObservedAt: long,
			Verdict: &reliantv1.PullRequestVerdict{}},
		{Number: 3, HeadSha: "c", State: "open", ObservedAt: long, Verdict: &reliantv1.PullRequestVerdict{Kind: "changes"}},
	}
	report := diffRecords("acme/widgets", recs, prrecords.DefaultLabelPolicy(), now)
	if report.Counts[prrecords.DiffAgree] != 1 || report.Counts[prrecords.DiffLabelStale] != 1 || report.Counts[prrecords.DiffLabelMissing] != 1 {
		t.Fatalf("counts = %v", report.Counts)
	}
	var out bytes.Buffer
	printDiffReport(&out, report, false)
	got := out.String()
	if !strings.Contains(got, "acme/widgets: 3 open; agree=1 label_missing=1 label_stale=1") ||
		strings.Contains(got, "#1\t") || !strings.Contains(got, "#2") || !strings.Contains(got, "#3") {
		t.Fatalf("report:\n%s", got)
	}
}
