// Copyright (c) 2026 Reliant Labs

package messageproto

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

const viewerSecret = "SECRET=hunter2-viewer"

// viewerClientBlocks decodes what ViewerMessageUpdate sent the way a client
// does, erring wide. The SDK (sdk/ts decodeChatUpdate) JSON.parses the
// payload, takes its "message" object when there is one, and hands that to
// protobuf-es fromJson, which reads a field by its proto name AND its JSON
// name (content_blocks and contentBlocks, appending both lists) and an enum
// as any integral number or its exact name. This reads both levels, both
// lists, and every block in them. ok is false when out is not a JSON object,
// which no client reads blocks from.
//
// It is written apart from the sanitizer on purpose: it is the oracle the
// sanitizer is held to, not a copy of it.
func viewerClientBlocks(t testing.TB, out []byte) (blocks []map[string]any, ok bool) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var payload map[string]any
	if dec.Decode(&payload) != nil || payload == nil {
		return nil, false
	}
	levels := []map[string]any{payload}
	if wrapped, isObject := payload["message"].(map[string]any); isObject {
		levels = append(levels, wrapped)
	}
	for _, level := range levels {
		for _, key := range []string{"content_blocks", "contentBlocks"} {
			list, _ := level[key].([]any)
			for _, b := range list {
				if block, isObject := b.(map[string]any); isObject {
					blocks = append(blocks, block)
				}
			}
		}
	}
	return blocks, true
}

// clientMayReadAs reports whether a decoded block "type" could name want to
// any client, erring wide: a JSON number equal to it in any form, its enum
// name or the name without its prefix in any case, or a string holding a
// number equal to it.
func clientMayReadAs(v any, want reliantv1.ContentBlockType) bool {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = string(x)
	case string:
		s = strings.TrimSpace(x)
		if strings.EqualFold(s, want.String()) || strings.EqualFold(s, strings.TrimPrefix(want.String(), "CONTENT_BLOCK_TYPE_")) {
			return true
		}
	default:
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && f == float64(want)
}

// clientStrings is every value a block carries under the proto and JSON
// spellings of one field. allStrings is false when one of them is present and
// not a string.
func clientStrings(block map[string]any, keys ...string) (values []string, allStrings bool) {
	allStrings = true
	for _, k := range keys {
		v, present := block[k]
		if !present || v == nil {
			continue
		}
		s, isString := v.(string)
		if !isString {
			allStrings = false
			continue
		}
		values = append(values, s)
	}
	return values, allStrings
}

// oracleKeepsResult is the disclosure rule restated over decoded blocks: a
// viewer may read a tool result's content only when every name it is known
// by (its own, and every call's in the payload that shares one of its ids) is
// a built-in kept by exact name, and it is known by at least one.
func oracleKeepsResult(result map[string]any, blocks []map[string]any) bool {
	own, ok := clientStrings(result, "tool_name", "toolName")
	if !ok {
		return false
	}
	ids, ok := clientStrings(result, "tool_call_id", "toolCallId")
	if !ok {
		return false
	}
	var names []string
	for _, n := range own {
		if n != "" {
			names = append(names, n)
		}
	}
	for _, b := range blocks {
		if !clientMayReadAs(b["type"], reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL) {
			continue
		}
		callIDs, _ := clientStrings(b, "tool_call_id", "toolCallId")
		for _, id := range ids {
			if id == "" || !containsString(callIDs, id) {
				continue
			}
			callNames, ok := clientStrings(b, "tool_name", "toolName")
			if !ok || len(callNames) == 0 {
				return false
			}
			names = append(names, callNames...)
		}
	}
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if !viewerKeptResultTools[n] {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// viewerLeaks is every withheld body in out that a client could read: a tool
// result's content the disclosure rule does not keep, or a redacted-thinking
// block's ciphertext. Empty when ViewerMessageUpdate did its job.
func viewerLeaks(t testing.TB, out []byte) []string {
	t.Helper()
	blocks, ok := viewerClientBlocks(t, out)
	if !ok {
		return nil
	}
	var leaks []string
	for _, b := range blocks {
		content, present := b["content"]
		if !present || content == nil {
			continue
		}
		switch {
		case clientMayReadAs(b["type"], reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT):
			if !oracleKeepsResult(b, blocks) {
				leaks = append(leaks, "tool result content "+marshalOrEmpty(content))
			}
		case clientMayReadAs(b["type"], reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING):
			if content != "" {
				leaks = append(leaks, "redacted thinking content "+marshalOrEmpty(content))
			}
		}
	}
	return leaks
}

// withheldSpellings is, for each block type a viewer never reads the body
// of, the JSON spellings of one such block a client could parse as that type:
// whitespace, number forms, the enum name in its spellings, escaped keys and
// values, the JSON field names protobuf-es also reads, duplicate keys, both
// block lists and both levels. %s is where the block's type goes, %c its
// content (viewerSecret, JSON-quoted).
var withheldSpellings = map[string]string{
	"compact":                 `{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","content":%c}]}`,
	"space after colon":       `{"id":"m","content_blocks":[{"type": %s,"tool_call_id":"1","content":%c}]}`,
	"space before colon":      `{"id":"m","content_blocks":[{"type" :%s,"tool_call_id":"1","content":%c}]}`,
	"spaces everywhere":       `{ "id" : "m" , "content_blocks" : [ { "type" : %s , "tool_call_id" : "1" , "content" : %c } ] }`,
	"newline":                 "{\"id\":\"m\",\"content_blocks\":[{\"type\":\n%s,\"tool_call_id\":\"1\",\"content\":%c}]}",
	"tab":                     "{\"id\":\"m\",\"content_blocks\":[{\"type\"\t:\t%s,\"tool_call_id\":\"1\",\"content\":%c}]}",
	"crlf":                    "{\"id\":\"m\",\"content_blocks\":[{\"type\":\r\n%s,\"tool_call_id\":\"1\",\"content\":\r\n%c}]}",
	"indented, wrapped":       "{\n  \"message\": {\n    \"id\": \"m\",\n    \"content_blocks\": [\n      {\n        \"type\": %s,\n        \"tool_call_id\": \"1\",\n        \"content\": %c\n      }\n    ]\n  }\n}",
	"escaped type key":        `{"id":"m","content_blocks":[{"\u0074ype":%s,"tool_call_id":"1","content":%c}]}`,
	"escaped content key":     `{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","\u0063ontent":%c}]}`,
	"escaped blocks key":      `{"id":"m","content\u005fblocks":[{"type":%s,"tool_call_id":"1","content":%c}]}`,
	"escaped message key":     `{"\u006dessage":{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","content":%c}]}}`,
	"JSON name contentBlocks": `{"id":"m","contentBlocks":[{"type":%s,"toolCallId":"1","content":%c}]}`,
	"JSON names, wrapped":     `{"message":{"id":"m","contentBlocks":[{"type":%s,"toolCallId":"1","content":%c}]}}`,
	"both lists":              `{"id":"m","content_blocks":[],"contentBlocks":[{"type":%s,"tool_call_id":"1","content":%c}]}`,
	"both levels":             `{"content_blocks":[{"type":%s,"content":%c}],"message":{"content_blocks":[{"type": %s,"content":%c}]}}`,
	"type after content":      `{"id":"m","content_blocks":[{"content":%c,"tool_call_id":"1","type":%s}]}`,
	"duplicate content":       `{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","content":"","content":%c}]}`,
	"duplicate type":          `{"id":"m","content_blocks":[{"type":1,"tool_call_id":"1","content":%c,"type":%s}]}`,
	"after non-objects":       `{"id":"m","content_blocks":[null,3,"x",[],{"type":%s,"tool_call_id":"1","content":%c}]}`,
	"non-string content":      `{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","content":[%c]}]}`,
	"escaped content value":   `{"id":"m","content_blocks":[{"type":%s,"tool_call_id":"1","content":"SECRET\u003dhunter2-viewer"}]}`,
	"minimal":                 `{"content_blocks":[{"type":%s,"content":%c}]}`,
}

// typeSpellings are the ways a block's "type" can name t.
func typeSpellings(t reliantv1.ContentBlockType) map[string]string {
	n := int(t)
	return map[string]string{
		"number":             strconv.Itoa(n),
		"N.0":                strconv.Itoa(n) + ".0",
		"Ne0":                strconv.Itoa(n) + "e0",
		"N0e-1":              strconv.Itoa(n) + "0e-1",
		"0.NE+1":             "0." + strconv.Itoa(n) + "E+1",
		"N.0000000000000001": strconv.Itoa(n) + ".0000000000000001",
		"name":               jsonString(t.String()),
		"name, escaped":      `"` + strings.Replace(t.String(), "_", `\u005f`, 1) + `"`,
		"name, lower case":   jsonString(strings.ToLower(t.String())),
		"name, short":        jsonString(strings.TrimPrefix(t.String(), "CONTENT_BLOCK_TYPE_")),
		"numeric string":     jsonString(strconv.Itoa(n)),
	}
}

// withheldBlockTypes is every block type whose body a viewer never reads in a
// message update: a tool result (of anything but a kept built-in; these name
// no tool, so none is kept) and redacted thinking (ciphertext, no client's).
var withheldBlockTypes = []reliantv1.ContentBlockType{
	reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
	reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_REDACTED_THINKING,
}

func spell(payload, typ string) string {
	return strings.NewReplacer("%s", typ, "%c", jsonString(viewerSecret)).Replace(payload)
}

// Whatever JSON spelling a stored message update uses for a block a viewer
// may not read the body of, the viewer is sent the block without that body:
// none of the withheld text in the bytes, and nothing a client could decode
// as that block's content. The block itself still goes (the call, its
// position and that it ran are the viewer's).
func TestViewerMessageUpdateWithholdsInAnySpelling(t *testing.T) {
	t.Parallel()
	for _, bt := range withheldBlockTypes {
		for typName, typ := range typeSpellings(bt) {
			for name, payload := range withheldSpellings {
				raw := []byte(spell(payload, typ))
				blocks, ok := viewerClientBlocks(t, raw)
				require.True(t, ok, "%s / %s: the fixture must be a JSON object: %s", typName, name, raw)
				require.NotEmpty(t, blocks, "%s / %s: the fixture must hold a block: %s", typName, name, raw)

				out := ViewerMessageUpdate(raw)
				assert.NotContains(t, string(out), viewerSecret, "%v, type %s, %s", bt, typName, name)
				assert.Empty(t, viewerLeaks(t, out), "%v, type %s, %s: %s", bt, typName, name, out)
				sent, ok := viewerClientBlocks(t, out)
				if assert.True(t, ok, "%v, type %s, %s: %s", bt, typName, name, out) {
					assert.Len(t, sent, len(blocks), "%v, type %s, %s: every block is still sent", bt, typName, name)
				}
			}
		}
	}
}

// The plan and question results a viewer reads whole stay whole in the same
// spellings, by either name field: the decode withholds what it must, no more.
func TestViewerMessageUpdateKeepsBuiltInResultsInAnySpelling(t *testing.T) {
	t.Parallel()
	const answer = `{"answer":"yes"}`
	for name, raw := range map[string]string{
		"spaced type":          `{"content_blocks":[{"type": 3,"tool_name":"ask_user","content":` + jsonString(answer) + `}]}`,
		"name type":            `{"content_blocks":[{"type":"CONTENT_BLOCK_TYPE_TOOL_RESULT","tool_name":"ask_user","content":` + jsonString(answer) + `}]}`,
		"JSON names":           `{"contentBlocks":[{"type":3,"toolName":"ask_user","content":` + jsonString(answer) + `}]}`,
		"call in view, spaced": `{"message":{"content_blocks":[{"type" : 2,"tool_call_id":"1","tool_name":"get_plan"},{"type": 3.0,"toolCallId":"1","content":` + jsonString(answer) + `}]}}`,
	} {
		out := ViewerMessageUpdate([]byte(raw))
		blocks, ok := viewerClientBlocks(t, out)
		require.True(t, ok, name)
		var kept bool
		for _, b := range blocks {
			if b["content"] == answer {
				kept = true
			}
		}
		assert.True(t, kept, "%s: a kept built-in's result: %s", name, out)
	}
}

// A result is known by every name it carries, in either field spelling, and
// by every call that shares either spelling of its id: a built-in on one and
// a third party's on another is elided, as it is when the names are spelled
// the one way (TestViewerElidesResultsOfMixedOrUnnamedTools).
func TestViewerMessageUpdateJoinsEveryNameSpelling(t *testing.T) {
	t.Parallel()
	c := jsonString(viewerSecret)
	for name, raw := range map[string]string{
		"result names disagree":          `{"content_blocks":[{"type":3,"tool_name":"get_plan","toolName":"bash","content":` + c + `}]}`,
		"call by JSON-name id":           `{"content_blocks":[{"type":2,"toolCallId":"1","toolName":"bash"},{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"result by JSON-name id":         `{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":"bash"},{"type":3,"toolCallId":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"two calls share the id":         `{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":"get_plan"},{"type":2,"tool_call_id":"1","tool_name":"bash"},{"type":3,"tool_call_id":"1","content":` + c + `}]}`,
		"call in the other list":         `{"contentBlocks":[{"type":2,"tool_call_id":"1","tool_name":"bash"}],"content_blocks":[{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"non-string name":                `{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":"get_plan"},{"type":3,"tool_call_id":"1","toolName":7,"content":` + c + `}]}`,
		"non-string id":                  `{"content_blocks":[{"type":3,"tool_call_id":["1"],"tool_name":"get_plan","content":` + c + `}]}`,
		"call names no tool":             `{"content_blocks":[{"type":2,"tool_call_id":"1"},{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"call type spelled as name":      `{"content_blocks":[{"type":"CONTENT_BLOCK_TYPE_TOOL_CALL","tool_call_id":"1","tool_name":"bash"},{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"call type spelled 2.0, spaced":  `{"content_blocks":[{"type": 2.0,"tool_call_id":"1","tool_name":"bash"},{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}`,
		"call at the top, result nested": `{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":"bash"}],"message":{"content_blocks":[{"type":3,"tool_call_id":"1","tool_name":"get_plan","content":` + c + `}]}}`,
	} {
		out := ViewerMessageUpdate([]byte(raw))
		assert.NotContains(t, string(out), viewerSecret, "%s: %s", name, out)
		assert.Empty(t, viewerLeaks(t, out), "%s: %s", name, out)
	}
}

// The redacted-thinking blanking every client gets (PreviewMessageUpdate)
// reads the same block lists the viewer's does: protobuf-es reads a block
// under contentBlocks as one under content_blocks.
func TestPreviewMessageUpdateBlanksRedactedThinkingUnderEveryListSpelling(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"JSON name contentBlocks", "JSON names, wrapped", "both lists", "escaped blocks key"} {
		for _, typ := range []string{"8", "8.0", `"CONTENT_BLOCK_TYPE_REDACTED_THINKING"`} {
			raw := []byte(spell(withheldSpellings[name], typ))
			for _, mode := range []reliantv1.ToolBodyMode{unspecified, full, stub} {
				out := PreviewMessageUpdate(raw, mode)
				assert.NotContains(t, string(out), viewerSecret, "%s, type %s, mode %v", name, typ, mode)
				assert.Empty(t, viewerLeaks(t, out), "%s, type %s, mode %v: %s", name, typ, mode, out)
			}
		}
	}
}

// FuzzViewerMessageUpdateNeverShipsAWithheldBody: whatever a stored message
// update holds, nothing a viewer's client could read as a withheld block
// leaves with its body: a tool result only when every name it is known by is
// a kept built-in, redacted thinking only blank.
func FuzzViewerMessageUpdateNeverShipsAWithheldBody(f *testing.F) {
	for _, payload := range withheldSpellings {
		f.Add([]byte(spell(payload, "3")))
		f.Add([]byte(spell(payload, "8.0")))
	}
	f.Add([]byte(`{"content_blocks":[{"type":2,"tool_call_id":"1","tool_name":"ask_user"},{"type": 3,"tool_call_id":"1","content":"x"}]}`))
	f.Add([]byte(`{"contentBlocks":[{"type":3,"toolName":"get_plan","tool_name":"bash","content":"x"}]}`))
	f.Add([]byte(`{"message":{"content_blocks":[{"type":3,"content":"x"}]}}0`)) // trailing bytes after the object
	f.Fuzz(func(t *testing.T, raw []byte) {
		out := ViewerMessageUpdate(raw)
		if leaks := viewerLeaks(t, out); len(leaks) > 0 {
			t.Fatalf("a withheld body reached a viewer: %v\nin:  %s\nout: %s", leaks, raw, out)
		}
	})
}
