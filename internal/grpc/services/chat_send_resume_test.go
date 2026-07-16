// Copyright (c) 2025 Reliant Labs
package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plain-message question-resume answer MUST be wrapped with the exact resume
// marker so the resumed LLM knows its tool-call "answer" is a post-failure
// resume, not a direct answer. This is a fixed content contract.
func TestMarkResumeAnswer_ExactMarker(t *testing.T) {
	assert.Equal(t,
		"<system> workflow was canceled or failed. user resumed with message</system>: please continue",
		markResumeAnswer("please continue"))
}

// questionResumeResponseData must place the (marked) answer in answers[0].freetext
// so the workflow's parseQuestionResponse reads it as feedback and the marker
// reaches the LLM.
func TestQuestionResumeResponseData_CarriesMarkerAsFreetext(t *testing.T) {
	marked := markResumeAnswer("do the thing")
	data, err := questionResumeResponseData(marked)
	require.NoError(t, err)

	var parsed struct {
		Answers []struct {
			Question string   `json:"question"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	require.NoError(t, json.Unmarshal([]byte(data), &parsed))
	require.Len(t, parsed.Answers, 1)
	assert.Equal(t, marked, parsed.Answers[0].Freetext,
		"the delivered answer freetext must carry the exact resume marker")
	assert.Empty(t, parsed.Answers[0].Selected,
		"no option is selected — a freetext answer is feedback, so the workflow loop continues")
}
