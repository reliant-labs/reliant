// Copyright (c) 2025 Reliant Labs
package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/svcerr"
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

// A server fault carries the stack svcerr captured where it was built; forge
// groups by that origin frame, so the call-site stamp must stand aside for it.
// Otherwise every fault logged through logging.Error is "logging.Error"'s
// caller and distinct faults that one helper logs merge into a single issue.

func faultOrigin(name string) error { return svcerr.Internal(name + " failed") }
func faultElsewhere() error         { return svcerr.Internal("the other thing failed") }

func logFaultThroughHelper(err error) { Error("request failed", "error", err) }

func TestServerFaultGroupsByItsOriginNotTheLoggingSite(t *testing.T) {
	for _, entry := range setupEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			isolateLogging(t)
			read := entry.setup(t, Settings{Format: "json"})

			logFaultThroughHelper(faultOrigin("save"))
			logFaultThroughHelper(faultElsewhere())

			recs := jsonLines(t, read())
			require.Len(t, recs, 2)
			one, two := recs[0], recs[1]

			assert.Equal(t, "github.com/reliant-labs/reliant/internal/logging.faultOrigin", one[obsquery.LogKeyCodeFunction])
			assert.Equal(t, "github.com/reliant-labs/reliant/internal/logging.faultElsewhere", two[obsquery.LogKeyCodeFunction])
			assert.NotEqual(t, fingerprintOf(t, one), fingerprintOf(t, two),
				"two faults built at different sites are two issues even when one helper logs both")
			for _, rec := range recs {
				assert.NotEmpty(t, rec[observe.LogKeyExceptionStacktrace], "a server fault reports its stack")
			}
		})
	}
}

func TestRecordWithoutSvcerrStackStillUsesTheCallSite(t *testing.T) {
	for name, err := range map[string]error{
		"plain error":   errors.New("db down"),
		"wrapped plain": fmt.Errorf("saving: %w", errors.New("db down")),
		"no error":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			isolateLogging(t)
			read := setupEntryPoints[0].setup(t, Settings{Format: "json"})

			if err != nil {
				errorSiteWith(err)
			} else {
				Error("no error attached")
			}

			recs := jsonLines(t, read())
			require.Len(t, recs, 1)
			if err != nil {
				assert.Equal(t, "github.com/reliant-labs/reliant/internal/logging.errorSiteWith", recs[0][obsquery.LogKeyCodeFunction])
			} else {
				assert.Contains(t, fmt.Sprint(recs[0][obsquery.LogKeyCodeFunction]), "TestRecordWithoutSvcerrStackStillUsesTheCallSite")
			}
			assert.NotContains(t, recs[0], observe.LogKeyExceptionStacktrace)
		})
	}
}

func errorSiteWith(err error) { Error("request failed", "error", err) }

func TestStacktraceOnServerFaultsAndNotOnClientErrors(t *testing.T) {
	isolateLogging(t)
	read := setupEntryPoints[0].setup(t, Settings{Format: "json"})

	Error("server fault", "error", svcerr.Internal("boom"))
	Error("client error", "error", svcerr.InvalidArgument("bad input"))

	recs := jsonLines(t, read())
	require.Len(t, recs, 2)
	assert.Contains(t, fmt.Sprint(recs[0][observe.LogKeyExceptionStacktrace]), "TestStacktraceOnServerFaultsAndNotOnClientErrors")
	assert.NotEqual(t, "ERROR", recs[1]["level"], "the error-class policy demotes a client error before any grouping input is added")
	assert.NotContains(t, recs[1], observe.LogKeyExceptionStacktrace)
}
