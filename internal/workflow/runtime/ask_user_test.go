// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatAskUserResponse_SingleQuestion(t *testing.T) {
	t.Parallel()
	responseData := `{"answers":[{"question":"Which auth?","selected":["JWT"],"freetext":""}]}`
	result := formatAskUserResponse("reply", responseData)
	assert.Equal(t, "Q: Which auth?\nA: JWT", result)
}

func TestFormatAskUserResponse_MultipleQuestions(t *testing.T) {
	t.Parallel()
	responseData := `{"answers":[{"question":"Which auth?","selected":["JWT"]},{"question":"Which DB?","selected":["Postgres"]}]}`
	result := formatAskUserResponse("reply", responseData)
	assert.Contains(t, result, "Q: Which auth?\nA: JWT")
	assert.Contains(t, result, "Q: Which DB?\nA: Postgres")
}

func TestFormatAskUserResponse_WithFreetext(t *testing.T) {
	t.Parallel()
	responseData := `{"answers":[{"question":"Which auth?","selected":["JWT"],"freetext":"But also consider OAuth"}]}`
	result := formatAskUserResponse("reply", responseData)
	assert.Equal(t, "Q: Which auth?\nA: JWT (note: But also consider OAuth)", result)
}

func TestFormatAskUserResponse_FreetextOnly(t *testing.T) {
	t.Parallel()
	responseData := `{"answers":[{"question":"Any preferences?","selected":[],"freetext":"I want something custom"}]}`
	result := formatAskUserResponse("reply", responseData)
	assert.Equal(t, "Q: Any preferences?\nA: (note: I want something custom)", result)
}

func TestFormatAskUserResponse_EmptyResponseReply(t *testing.T) {
	t.Parallel()
	result := formatAskUserResponse("reply", "")
	assert.Equal(t, "The user replied via chat message. Check the conversation for their response.", result)
}

func TestFormatAskUserResponse_EmptyResponseContinue(t *testing.T) {
	t.Parallel()
	result := formatAskUserResponse("continue", "")
	assert.Equal(t, "The user continued without providing a specific answer.", result)
}

func TestFormatAskUserResponse_MalformedJSON(t *testing.T) {
	t.Parallel()
	result := formatAskUserResponse("reply", "{bad json")
	assert.Equal(t, "{bad json", result)
}

func TestFormatAskUserResponse_MultiSelect(t *testing.T) {
	responseData := `{"answers":[{"question":"Which features?","selected":["Auth","Logging","Metrics"]}]}`
	result := formatAskUserResponse("reply", responseData)
	assert.Equal(t, "Q: Which features?\nA: Auth, Logging, Metrics", result)
}
