// Copyright (c) 2025 Reliant Labs

// Package google_sheets_test drives every action in the embedded
// `google_sheets` manifest through the real declarative runner against an
// httptest fake of the Sheets API v4. The manifest is the one the binary ships
// (catalog.MustBuiltin), with only its base_url pointed at the fake, so these
// tests pin the request each action sends, the output it selects and how it
// classifies failures. Shapes follow Google's Sheets API discovery document
// (Spreadsheet, ValueRange, UpdateValuesResponse, AppendValuesResponse,
// BatchUpdateSpreadsheetResponse) and its JSON error model.
package google_sheets_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

const (
	token  = "ya29.fake-sheets-access-token-8d2c"
	prefix = "/v4"
	sid    = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"
)

type recorded struct {
	Method      string
	Path        string // decoded
	EscapedPath string
	Query       url.Values
	Header      http.Header
	Body        map[string]any
}

type fakeSheets struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	got   []recorded
	reply func(w http.ResponseWriter, r recorded)
}

func newFake(t *testing.T) *fakeSheets {
	t.Helper()
	f := &fakeSheets{t: t}
	f.srv = netguardtest.NewTLSServer(t, http.HandlerFunc(f.serve))
	return f
}

func (f *fakeSheets) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone()}
	if len(raw) > 0 {
		require.NoError(f.t, json.Unmarshal(raw, &rec.Body), "request body must be JSON: %s", raw)
	}
	f.mu.Lock()
	f.got = append(f.got, rec)
	reply := f.reply
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if reply == nil {
		googleErr(w, 404, "NOT_FOUND", "notFound", "no reply configured")
		return
	}
	reply(w, rec)
}

func (f *fakeSheets) only() recorded {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(f.t, f.got, 1, "exactly one request")
	return f.got[0]
}

func (f *fakeSheets) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func googleErr(w http.ResponseWriter, code int, status, reason, message string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"error": map[string]any{
		"code": code, "message": message, "status": status,
		"errors": []any{map[string]any{"reason": reason, "domain": "global", "message": message}},
	}})
}

type staticCred struct{}

func (staticCred) ConnectionID() string      { return "conn_gsheets" }
func (staticCred) Params() map[string]string { return nil }
func (staticCred) Scrub(s string) string     { return strings.ReplaceAll(s, token, "[redacted]") }
func (staticCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type staticSource struct{}

func (staticSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return staticCred{}, nil
}

func shipped(t *testing.T) *reliantv1.IntegrationManifest {
	t.Helper()
	m, err := catalog.MustBuiltin().Manifest("google_sheets", 1)
	require.NoError(t, err)
	return m
}

func (f *fakeSheets) try(id string, params map[string]any) (*httpaction.Result, error) {
	f.t.Helper()
	m := proto.Clone(shipped(f.t)).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = f.srv.URL + prefix
	var action *reliantv1.ActionSpec
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			action = a
		}
	}
	require.NotNil(f.t, action, "google_sheets has no action %s", id)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	runner := httpaction.NewRunner(netguardtest.Guard()).WithRootCAs(pool)
	return runner.RunAuthenticated(context.Background(), m, action, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

func (f *fakeSheets) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeSheets) ok(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res := f.run(id, params)
	require.False(f.t, res.IsError, res.Content)
	return res
}

// ---------------------------------------------------------------------------
// The shipped declaration.
// ---------------------------------------------------------------------------

func TestManifestDeclaration(t *testing.T) {
	m := shipped(t)
	oauth := m.GetConnection().GetAuth()[0].GetOauth2()
	assert.Equal(t, []string{"https://www.googleapis.com/auth/spreadsheets", "openid", "email"}, oauth.GetScopes(),
		"spreadsheets for the API; openid and email only so the probe can name the account")
	assert.Equal(t, "S256", oauth.GetPkce())
	assert.Equal(t, "offline", oauth.GetAuthorizeParams()["access_type"])
	assert.Equal(t, "consent", oauth.GetAuthorizeParams()["prompt"])
	assert.Equal(t, "https://sheets.googleapis.com/v4", m.GetConnection().GetBaseUrl())

	// Sheets has no profile endpoint: the probe is OpenID Connect userinfo, on
	// the one other Google host the credential may reach.
	probe := m.GetConnection().GetProbe()
	assert.Equal(t, "https://openidconnect.googleapis.com/v1/userinfo", probe.GetUrl())
	assert.Equal(t, "response.sub", probe.GetExternalId())
	assert.Equal(t, []string{"openidconnect.googleapis.com"}, m.GetConnection().GetAllowedHosts())

	mutates := map[string]bool{
		"spreadsheet.create": true, "spreadsheet.get": false, "values.get": false, "values.batch_get": false,
		"values.update": true, "values.append": true, "values.clear": true, "sheet.add": true,
	}
	require.Len(t, m.GetActions(), len(mutates))
	for _, a := range m.GetActions() {
		want, known := mutates[a.GetId()]
		require.True(t, known, "unexpected action %s", a.GetId())
		assert.Equal(t, want, a.GetMutates(), a.GetId())
		assert.Equal(t, "server", a.GetPlacement(), "%s holds a credential, so it runs on the server", a.GetId())
		assert.True(t, a.GetTool().GetExpose(), a.GetId())
		assert.NotEmpty(t, a.GetKeywords(), a.GetId())
		raw, err := json.Marshal(a.GetParams().AsMap())
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"connection"`, "%s takes a connection", a.GetId())
	}
}

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

func TestSpreadsheetGetNeverAsksForGridData(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{
			"spreadsheetId": sid, "spreadsheetUrl": "https://docs.google.com/spreadsheets/d/" + sid + "/edit",
			"properties": map[string]any{"title": "Q4 pipeline", "locale": "en_US", "timeZone": "America/New_York"},
			"sheets": []any{
				map[string]any{"properties": map[string]any{"sheetId": 0, "title": "Leads", "index": 0, "gridProperties": map[string]any{"rowCount": 1000, "columnCount": 26}}},
				map[string]any{"properties": map[string]any{"sheetId": 1873, "title": "Customers", "index": 1}},
			},
		})
	}
	res := f.ok("spreadsheet.get", map[string]any{"spreadsheet_id": sid})

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid, req.Path)
	assert.Equal(t, "false", req.Query.Get("includeGridData"))
	assert.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))

	assert.Equal(t, sid, res.Data["spreadsheet_id"])
	assert.Equal(t, "Q4 pipeline", res.Data["title"])
	assert.Equal(t, "https://docs.google.com/spreadsheets/d/"+sid+"/edit", res.Data["url"])
	assert.Equal(t, []any{
		map[string]any{"sheet_id": 0.0, "title": "Leads", "index": 0.0, "row_count": 1000.0, "column_count": 26.0},
		map[string]any{"sheet_id": 1873.0, "title": "Customers", "index": 1.0, "row_count": nil, "column_count": nil},
	}, res.Data["sheets"])
}

func TestValuesGetEscapesTheRangeIntoThePath(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"range": "'Q4 pipeline'!A1:C2", "majorDimension": "ROWS",
			"values": []any{[]any{"Name", "Qty", "Price"}, []any{"Widget", "3"}}})
	}
	res := f.ok("values.get", map[string]any{
		"spreadsheet_id": sid, "range": "'Q4 pipeline'!A1:C5", "value_render_option": "UNFORMATTED_VALUE",
	})

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values/'Q4 pipeline'!A1:C5", req.Path, "the range arrives decoded: spaces, quotes and ! survive the round trip")
	assert.Equal(t, "UNFORMATTED_VALUE", req.Query.Get("valueRenderOption"))
	assert.Equal(t, "ROWS", req.Query.Get("majorDimension"))

	assert.Equal(t, "'Q4 pipeline'!A1:C2", res.Data["range"], "the range Google resolved")
	assert.Equal(t, []any{[]any{"Name", "Qty", "Price"}, []any{"Widget", "3"}}, res.Data["values"], "rows keep their own lengths")
}

// Path expressions are escaped by the shared runner, not by the manifest.
// Check the wire path: URL.Path is decoded and would hide this protection.
func TestRangesCannotChangeRouting(t *testing.T) {
	const a1 = "'Sheet1/../../other?#&ranges=evil%2F'!A1:B2"
	for action, suffix := range map[string]string{
		"values.get": "", "values.update": "", "values.append": ":append", "values.clear": ":clear",
	} {
		t.Run(action, func(t *testing.T) {
			f := newFake(t)
			f.reply = func(w http.ResponseWriter, _ recorded) { writeJSON(w, map[string]any{}) }
			params := map[string]any{"spreadsheet_id": sid, "range": a1}
			if action == "values.update" || action == "values.append" {
				params["values"] = []any{[]any{"x"}}
			}
			f.ok(action, params)
			req := f.only()
			assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values/"+url.PathEscape(a1)+suffix, req.EscapedPath)
			assert.NotContains(t, req.Query, "ranges")
		})
	}
	t.Run("values.batch_get", func(t *testing.T) {
		f := newFake(t)
		f.reply = func(w http.ResponseWriter, _ recorded) { writeJSON(w, map[string]any{}) }
		f.ok("values.batch_get", map[string]any{"spreadsheet_id": sid, "ranges": []any{a1, "Sheet2!A1"}})
		req := f.only()
		assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values:batchGet", req.EscapedPath)
		assert.Equal(t, []string{a1, "Sheet2!A1"}, req.Query["ranges"])
		assert.NotContains(t, req.Query, "evil")
	})
}

func TestValuesGetOfAnEmptyRange(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"range": "Sheet1!A1:B2", "majorDimension": "ROWS"})
	}
	res := f.ok("values.get", map[string]any{"spreadsheet_id": sid, "range": "Sheet1!A1:B2"})
	assert.Equal(t, []any{}, res.Data["values"], "Google omits values for an empty range")
	assert.Equal(t, "FORMATTED_VALUE", f.only().Query.Get("valueRenderOption"))
}

func TestValuesBatchGetRepeatsRanges(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "valueRanges": []any{
			map[string]any{"range": "Sheet1!A1:B2", "majorDimension": "COLUMNS", "values": []any{[]any{"a", "c"}, []any{"b", "d"}}},
			map[string]any{"range": "Totals!A1", "majorDimension": "COLUMNS"},
		}})
	}
	res := f.ok("values.batch_get", map[string]any{
		"spreadsheet_id": sid, "ranges": []any{"Sheet1!A1:B2", "Totals!A1"}, "major_dimension": "COLUMNS",
	})

	req := f.only()
	assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values:batchGet", req.Path)
	assert.Equal(t, []string{"Sheet1!A1:B2", "Totals!A1"}, req.Query["ranges"], "one ranges parameter per range, as Google takes them")
	assert.Equal(t, "COLUMNS", req.Query.Get("majorDimension"))

	assert.Equal(t, sid, res.Data["spreadsheet_id"])
	assert.Equal(t, []any{
		map[string]any{"range": "Sheet1!A1:B2", "major_dimension": "COLUMNS", "values": []any{[]any{"a", "c"}, []any{"b", "d"}}},
		map[string]any{"range": "Totals!A1", "major_dimension": "COLUMNS", "values": []any{}},
	}, res.Data["value_ranges"])
}

// ---------------------------------------------------------------------------
// Writes.
// ---------------------------------------------------------------------------

func TestSpreadsheetCreate(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{
			"spreadsheetId": "NEWID1234567890", "spreadsheetUrl": "https://docs.google.com/spreadsheets/d/NEWID1234567890/edit",
			"properties": map[string]any{"title": "Q4 pipeline"},
			"sheets": []any{
				map[string]any{"properties": map[string]any{"sheetId": 0, "title": "Leads", "index": 0}},
				map[string]any{"properties": map[string]any{"sheetId": 11, "title": "Customers", "index": 1}},
			},
		})
	}
	res := f.ok("spreadsheet.create", map[string]any{"title": "Q4 pipeline", "sheet_names": []any{"Leads", "Customers"}})

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, prefix+"/spreadsheets", req.Path)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, map[string]any{
		"properties": map[string]any{"title": "Q4 pipeline"},
		"sheets": []any{
			map[string]any{"properties": map[string]any{"title": "Leads"}},
			map[string]any{"properties": map[string]any{"title": "Customers"}},
		},
	}, req.Body)
	assert.Equal(t, "NEWID1234567890", res.Data["spreadsheet_id"])
	assert.Len(t, res.Data["sheets"], 2)
}

func TestSpreadsheetCreateWithoutTabNamesLeavesGoogleTheDefault(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": "NEWID1234567890", "properties": map[string]any{"title": "Notes"}})
	}
	res := f.ok("spreadsheet.create", map[string]any{"title": "Notes"})
	assert.Equal(t, map[string]any{"properties": map[string]any{"title": "Notes"}}, f.only().Body, "no sheets key: Google adds Sheet1")
	assert.Equal(t, []any{}, res.Data["sheets"])
}

func TestValuesUpdateDefaultsToUserEntered(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "updatedRange": "Sheet1!A1:B2", "updatedRows": 2, "updatedColumns": 2, "updatedCells": 4})
	}
	values := []any{[]any{"Name", "Qty"}, []any{"Widget", 3.0}}
	res := f.ok("values.update", map[string]any{"spreadsheet_id": sid, "range": "Sheet1!A1", "values": values})

	req := f.only()
	assert.Equal(t, "PUT", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values/Sheet1!A1", req.Path)
	assert.Equal(t, "USER_ENTERED", req.Query.Get("valueInputOption"), "formulas and dates are parsed unless RAW is asked for")
	assert.Equal(t, map[string]any{"range": "Sheet1!A1", "majorDimension": "ROWS", "values": values}, req.Body)

	assert.Equal(t, map[string]any{
		"spreadsheet_id": sid, "updated_range": "Sheet1!A1:B2", "updated_rows": 2.0, "updated_columns": 2.0, "updated_cells": 4.0,
	}, res.Data)
}

func TestValuesUpdateRaw(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "updatedCells": 1})
	}
	f.ok("values.update", map[string]any{
		"spreadsheet_id": sid, "range": "Sheet1!A1", "values": []any{[]any{"=SUM(A2:A9)"}}, "value_input_option": "RAW",
	})
	assert.Equal(t, "RAW", f.only().Query.Get("valueInputOption"))
}

func TestValuesAppendInsertsRows(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "tableRange": "Sheet1!A1:C4",
			"updates": map[string]any{"spreadsheetId": sid, "updatedRange": "Sheet1!A5:C5", "updatedRows": 1, "updatedColumns": 3, "updatedCells": 3}})
	}
	row := []any{"Acme", nil, true}
	res := f.ok("values.append", map[string]any{"spreadsheet_id": sid, "range": "Sheet1", "values": []any{row}})

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values/Sheet1:append", req.Path, "the :append verb follows the range")
	assert.Equal(t, "USER_ENTERED", req.Query.Get("valueInputOption"))
	assert.Equal(t, "INSERT_ROWS", req.Query.Get("insertDataOption"))
	assert.Equal(t, map[string]any{"majorDimension": "ROWS", "values": []any{row}}, req.Body, "null leaves a cell empty and is sent as null")

	assert.Equal(t, map[string]any{
		"spreadsheet_id": sid, "table_range": "Sheet1!A1:C4", "updated_range": "Sheet1!A5:C5",
		"updated_rows": 1.0, "updated_columns": 3.0, "updated_cells": 3.0,
	}, res.Data)
}

func TestValuesAppendRefusesBadParamsBeforeCallingGoogle(t *testing.T) {
	f := newFake(t)
	for name, params := range map[string]map[string]any{
		"no values":         {"spreadsheet_id": sid, "range": "Sheet1"},
		"empty values":      {"spreadsheet_id": sid, "range": "Sheet1", "values": []any{}},
		"flat values":       {"spreadsheet_id": sid, "range": "Sheet1", "values": []any{"a", "b"}},
		"object cell":       {"spreadsheet_id": sid, "range": "Sheet1", "values": []any{[]any{map[string]any{"a": 1}}}},
		"bad insert option": {"spreadsheet_id": sid, "range": "Sheet1", "values": []any{[]any{"a"}}, "insert_data_option": "APPEND"},
		"spreadsheet url":   {"spreadsheet_id": "https://docs.google.com/spreadsheets/d/" + sid, "range": "Sheet1", "values": []any{[]any{"a"}}},
		"path-like id":      {"spreadsheet_id": "../" + sid, "range": "Sheet1", "values": []any{[]any{"a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.try("values.append", params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid params")
		})
	}
	assert.Zero(t, f.count(), "nothing was appended")
}

func TestValuesClear(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "clearedRange": "Sheet1!A2:D1000"})
	}
	res := f.ok("values.clear", map[string]any{"spreadsheet_id": sid, "range": "Sheet1!A2:D"})

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid+"/values/Sheet1!A2:D:clear", req.Path)
	assert.Empty(t, req.Body, "clear takes an empty body")
	assert.Equal(t, map[string]any{"spreadsheet_id": sid, "cleared_range": "Sheet1!A2:D1000"}, res.Data)
}

func TestSheetAddIsABatchUpdateAddSheet(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"spreadsheetId": sid, "replies": []any{
			map[string]any{"addSheet": map[string]any{"properties": map[string]any{"sheetId": 482915, "title": "Q4 pipeline", "index": 2}}},
		}})
	}
	res := f.ok("sheet.add", map[string]any{"spreadsheet_id": sid, "title": "Q4 pipeline", "row_count": 200.0, "column_count": 12.0})

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, prefix+"/spreadsheets/"+sid+":batchUpdate", req.Path)
	assert.Equal(t, map[string]any{"requests": []any{
		map[string]any{"addSheet": map[string]any{"properties": map[string]any{
			"title": "Q4 pipeline", "gridProperties": map[string]any{"rowCount": 200.0, "columnCount": 12.0},
		}}},
	}}, req.Body)
	assert.Equal(t, map[string]any{"spreadsheet_id": sid, "sheet_id": 482915.0, "title": "Q4 pipeline", "index": 2.0}, res.Data)
}

func TestSheetAddWithoutSizeSendsNoGridProperties(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		writeJSON(w, map[string]any{"replies": []any{map[string]any{"addSheet": map[string]any{"properties": map[string]any{"sheetId": 7, "title": "Notes"}}}}})
	}
	f.ok("sheet.add", map[string]any{"spreadsheet_id": sid, "title": "Notes"})
	assert.Equal(t, map[string]any{"requests": []any{
		map[string]any{"addSheet": map[string]any{"properties": map[string]any{"title": "Notes"}}},
	}}, f.only().Body)
}

// ---------------------------------------------------------------------------
// Errors: Google's error model, classified once for every action.
// ---------------------------------------------------------------------------

func TestErrorsAreClassifiedForEveryAction(t *testing.T) {
	cell := []any{[]any{"a"}}
	calls := map[string]map[string]any{
		"spreadsheet.create": {"title": "x"},
		"spreadsheet.get":    {"spreadsheet_id": sid},
		"values.get":         {"spreadsheet_id": sid, "range": "Sheet1!A1"},
		"values.batch_get":   {"spreadsheet_id": sid, "ranges": []any{"Sheet1!A1"}},
		"values.update":      {"spreadsheet_id": sid, "range": "Sheet1!A1", "values": cell},
		"values.append":      {"spreadsheet_id": sid, "range": "Sheet1", "values": cell},
		"values.clear":       {"spreadsheet_id": sid, "range": "Sheet1!A1"},
		"sheet.add":          {"spreadsheet_id": sid, "title": "x"},
	}
	cases := []struct {
		name      string
		status    int
		reason    string
		retryable bool
		contains  string
	}{
		{"rate limited", 403, "userRateLimitExceeded", true, "rate limit"},
		{"scope missing", 403, "insufficientPermissions", false, "reconnect Google Sheets and allow every permission"},
		{"forbidden", 403, "forbidden", false, "refused"},
		{"quota", 429, "rateLimitExceeded", true, "rate limit"},
		{"bad range", 400, "badRequest", false, "invalid"},
		{"not found", 404, "notFound", false, "not found"},
		{"backend", 503, "backendError", true, ""},
	}
	for _, tc := range cases {
		for action, params := range calls {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				f := newFake(t)
				f.reply = func(w http.ResponseWriter, _ recorded) {
					googleErr(w, tc.status, "X", tc.reason, "Unable to parse range: Sheet9!A1 ("+token+")")
				}
				res := f.run(action, params)
				assert.True(t, res.IsError)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, res.Content)
				if tc.contains != "" {
					assert.Contains(t, res.Content, tc.contains)
				}
				assert.NotContains(t, res.Content, token, "the credential is scrubbed from an echoing error")
			})
		}
	}
}

func TestBadRangeSurfacesGooglesMessage(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		googleErr(w, 400, "INVALID_ARGUMENT", "badRequest", "Unable to parse range: Sheet9!A1")
	}
	res := f.run("values.get", map[string]any{"spreadsheet_id": sid, "range": "Sheet9!A1"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "Unable to parse range: Sheet9!A1")
}

func TestUnauthorizedSaysReconnect(t *testing.T) {
	f := newFake(t)
	f.reply = func(w http.ResponseWriter, _ recorded) {
		googleErr(w, 401, "UNAUTHENTICATED", "authError", "Invalid Credentials")
	}
	res := f.run("values.get", map[string]any{"spreadsheet_id": sid, "range": "Sheet1!A1"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Equal(t, 401, res.StatusCode)
	assert.Contains(t, res.Content, "reconnect Google Sheets")
}
