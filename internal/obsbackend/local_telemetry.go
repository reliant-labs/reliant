package obsbackend

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// TelemetryArrivalRequest asks whether declared systems' telemetry is
// arriving (`forge obs telemetry`): Narrow.Services are the systems'
// service.names.
type TelemetryArrivalRequest struct {
	Narrow Narrow
	// Platform reads rows of every env: a scope "platform" system's signals
	// carry no env identity, so an env predicate would match none of them.
	Platform bool
}

// TelemetrySignalArrival is when one service last sent one signal, and how
// many rows of it the range holds.
type TelemetrySignalArrival struct {
	Service  string
	Signal   string
	LastSeen time.Time
	Rows     int64
}

// TelemetryScrapeResult is the newest result of one scraped endpoint.
type TelemetryScrapeResult struct {
	Service  string
	Instance string
	// Up is 1 when the endpoint's newest scrape succeeded, 0 when it failed.
	Up float64
	// Samples is what the scrape returned; Kept what its allow/deny lists
	// kept.
	Samples, Kept float64
	LastSeen      time.Time
}

// TelemetryArrivalResult is a TelemetryArrival read.
type TelemetryArrivalResult struct {
	Signals []TelemetrySignalArrival
	Scrapes []TelemetryScrapeResult
	// Unread names each read that failed (the signal, or "scrapes"), with
	// why: a store that predates a table answers for the rest.
	Unread     map[string]string
	Statements []Statement
}

// TelemetryArrival reads, for each named service, the newest row of every
// signal (obsquery.TelemetryArrival) and the newest result of every scrape
// (obsquery.TelemetryScrapes). Each statement runs on its own, so a table the
// local stack lacks costs only its signal.
func (l Local) TelemetryArrival(ctx context.Context, req TelemetryArrivalRequest) (TelemetryArrivalResult, error) {
	env := l.Env
	if req.Platform {
		env = ""
	}
	sc := l.scopeFor(req.Narrow, env)
	res := TelemetryArrivalResult{Unread: map[string]string{}}
	type read struct {
		name string
		st   obsquery.Stmt
	}
	var reads []read
	for _, sig := range obsquery.ArrivalSignals {
		st, err := obsquery.TelemetryArrival{Scope: sc, Signal: sig}.Compile()
		if err != nil {
			return res, compileErr(err)
		}
		reads = append(reads, read{sig, st})
	}
	st, err := obsquery.TelemetryScrapes{Scope: sc}.Compile()
	if err != nil {
		return res, compileErr(err)
	}
	reads = append(reads, read{"scrapes", st})
	for _, r := range reads {
		res.Statements = append(res.Statements, stmt("telemetry "+r.name, r.st))
	}
	if req.Narrow.Explain {
		return res, nil
	}
	for _, r := range reads {
		out, err := l.run(ctx, r.st, DefaultLimits())
		if err != nil {
			res.Unread[r.name] = err.Error()
			continue
		}
		for _, row := range out.Rows {
			if r.name == "scrapes" {
				if len(row) < 6 {
					res.Unread[r.name] = "the local ClickStack returned too few columns"
					break
				}
				res.Scrapes = append(res.Scrapes, TelemetryScrapeResult{Service: AsString(row[0]), Instance: AsString(row[1]),
					Up: AsFloat(row[2]), Samples: AsFloat(row[3]), Kept: AsFloat(row[4]), LastSeen: AsTime(row[5])})
				continue
			}
			if len(row) < 3 {
				res.Unread[r.name] = "the local ClickStack returned too few columns"
				break
			}
			res.Signals = append(res.Signals, TelemetrySignalArrival{Service: AsString(row[0]), Signal: r.name, LastSeen: AsTime(row[1]), Rows: AsInt(row[2])})
		}
	}
	return res, nil
}
