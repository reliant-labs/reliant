// Copyright (c) 2025 Reliant Labs
package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tlog "go.temporal.io/sdk/log"
)

// Two ERROR lines with the same words from two call sites are two issues:
// sfp1 hashes the call site, and the call site is only there if every Setup
// entry point stamps forge's error_inputs on the record.

func errorSiteOne() { Error("could not save the chat", "error", errors.New("db down")) }
func errorSiteTwo() { Error("could not save the chat", "error", errors.New("db down")) }

func fingerprintOf(t *testing.T, rec map[string]any) string {
	t.Helper()
	attrs := map[string]string{}
	for _, key := range []string{
		obsquery.LogKeyCodeFunction, obsquery.LogKeyCodeFile, "code.line.number",
		obsquery.LogKeyExceptionType, obsquery.LogKeyExceptionMessage,
	} {
		if v, ok := rec[key]; ok {
			attrs[key] = fmt.Sprint(v)
		}
	}
	return obsquery.ServerFingerprint(obsquery.ServerErrorRecord{
		ServiceName: "reliant-api-server",
		ScopeName:   "reliant-api-server",
		Body:        fmt.Sprint(rec["msg"]),
		Attributes:  attrs,
	})
}

func TestEverySetupStampsErrorInputs(t *testing.T) {
	for _, entry := range setupEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			isolateLogging(t)
			read := entry.setup(t, Settings{Format: "json"})

			errorSiteOne()
			errorSiteTwo()
			Info("not an error")

			recs := jsonLines(t, read())
			require.Len(t, recs, 3)
			one, two := recs[0], recs[1]

			assert.Equal(t, "github.com/reliant-labs/reliant/internal/logging.errorSiteOne", one[obsquery.LogKeyCodeFunction])
			assert.Equal(t, "github.com/reliant-labs/reliant/internal/logging.errorSiteTwo", two[obsquery.LogKeyCodeFunction])
			assert.True(t, strings.HasSuffix(fmt.Sprint(one[obsquery.LogKeyCodeFile]), "internal/logging/callsite_handler_test.go"), "%v", one[obsquery.LogKeyCodeFile])
			assert.NotZero(t, one["code.line.number"])
			assert.Equal(t, "*errors.errorString", one[obsquery.LogKeyExceptionType])
			assert.Equal(t, "db down", one[obsquery.LogKeyExceptionMessage])

			assert.NotEqual(t, fingerprintOf(t, one), fingerprintOf(t, two),
				"two call sites with the same message and error are two issues")

			for _, key := range []string{obsquery.LogKeyCodeFunction, obsquery.LogKeyCodeFile, obsquery.LogKeyExceptionType} {
				assert.NotContains(t, recs[2], key, "only ERROR records carry error inputs")
			}
		})
	}
}

func slogSiteOne(l *slog.Logger) { l.Error("history ends", "err", errors.New("boom")) }
func slogSiteTwo(l *slog.Logger) { l.Error("history ends", "err", errors.New("boom")) }

func temporalSiteOne(l tlog.Logger) { l.Error("history ends", "Error", errors.New("boom")) }
func temporalSiteTwo(l tlog.Logger) { l.Error("history ends", "Error", errors.New("boom")) }

func TestErrorInputsNameTheCallerForEveryFrontEnd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		one, two func(logger *slog.Logger)
		want     string
	}{
		{"slog.Logger", slogSiteOne, slogSiteTwo, "slogSite"},
		{
			"Temporal adapter",
			func(l *slog.Logger) { temporalSiteOne(tlog.NewStructuredLogger(l)) },
			func(l *slog.Logger) { temporalSiteTwo(tlog.NewStructuredLogger(l)) },
			"temporalSite",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLogging(t)
			read := setupEntryPoints[0].setup(t, Settings{Format: "json"})

			tc.one(slog.Default())
			tc.two(slog.Default())

			recs := jsonLines(t, read())
			require.Len(t, recs, 2)
			assert.True(t, strings.HasSuffix(fmt.Sprint(recs[0][obsquery.LogKeyCodeFunction]), "."+tc.want+"One"), "%v", recs[0][obsquery.LogKeyCodeFunction])
			assert.True(t, strings.HasSuffix(fmt.Sprint(recs[1][obsquery.LogKeyCodeFunction]), "."+tc.want+"Two"), "%v", recs[1][obsquery.LogKeyCodeFunction])
			assert.NotEqual(t, fingerprintOf(t, recs[0]), fingerprintOf(t, recs[1]))
		})
	}
}
