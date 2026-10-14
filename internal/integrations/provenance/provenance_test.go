// Copyright (c) 2025 Reliant Labs
package provenance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBodyDigestSurvivesWhatGitHubChangesInTransit(t *testing.T) {
	sent := "Looks good.\n\nOne nit.\n"
	received := "  Looks good.\r\n\r\nOne nit."
	assert.Equal(t, BodyDigest(sent), BodyDigest(received), "GitHub normalizes line endings and trims")
	assert.NotEqual(t, BodyDigest(sent), BodyDigest("Looks good.\n\nTwo nits."))
}

func TestBodyDigestIgnoresTheBotsSignature(t *testing.T) {
	footer := SignaturePrefix + "nightly triage, an automation of @octocat.</sub>"
	authored := "Looks good.\n\nOne nit."
	assert.Equal(t, BodyDigest(authored), BodyDigest(authored+"\n\n"+footer), "the ledger digests the text before it is signed, the echo after")
	assert.Equal(t, BodyDigest(authored), BodyDigest(authored+"\r\n\r\n"+footer+"\r\n"))
	assert.Equal(t, BodyDigest(""), BodyDigest(footer), "a body that is only the signature is empty")
	assert.NotEqual(t, BodyDigest(authored), BodyDigest(authored+"\n\n"+footer+"\n\nmore"), "only a TRAILING signature paragraph is ignored")
	assert.NotEqual(t, BodyDigest(authored), BodyDigest(authored+"\n\n"+SignaturePrefix+"x.</sub> and then some"), "text after the closing tag is not a signature")
}

func TestScopeIsCaseInsensitiveButBranchesAreNot(t *testing.T) {
	assert.Equal(t, "github:acme/app", ScopeOf("Acme/App"))
	assert.Equal(t, "github:acme/app#7", TargetOf(ScopeOf("Acme/App"), "7"))
	assert.Equal(t, "github:acme/app@Feature/X", BranchTargetOf(ScopeOf("Acme/App"), "Feature/X"))
}

func TestIDStringRendersEveryDecodedNumberTheSame(t *testing.T) {
	for _, v := range []any{json.Number("4242"), float64(4242), int64(4242), int(4242), "4242"} {
		assert.Equal(t, "4242", IDString(v), "%T", v)
	}
	assert.Equal(t, "", IDString(nil))
}

func TestRunTrailerReadsOnlyTheFinalParagraph(t *testing.T) {
	assert.Equal(t, "chat-1", RunTrailer("Fix it\n\nbody\n\nReliant-Run: chat-1\nSigned-off-by: x"))
	assert.Equal(t, "chat-1", RunTrailer("Fix it\n\nreliant-run: chat-1\n"))
	assert.Equal(t, "", RunTrailer("Fix it\n\nReliant-Run: chat-1\n\nand then some prose"), "a trailer mid-message is not a trailer")
	assert.Equal(t, "", RunTrailer("Reliant-Run in the subject"))
	assert.Equal(t, "", RunTrailer(""))
}

func TestCorrelationRoundTripsThroughTheLedgerColumn(t *testing.T) {
	c := Correlation{ObjectKind: KindReview, Events: []string{"pull_request_review.submitted"}, BodySHA256: "abc", ReviewState: "approved"}
	assert.Equal(t, c, CorrelationFromMap(c.Map()))
	assert.Equal(t, Correlation{}, CorrelationFromMap(nil))
}
