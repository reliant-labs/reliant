package dbbrowse

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/reliant-labs/forge/pkg/dbquery"
)

// Page is one page of a table.
type Page struct {
	// Columns and Rows are dbquery's: each row has one cell per column, a
	// nil, bool, json.Number, string or json.RawMessage (see
	// [dbquery.Result]).
	Columns []dbquery.Column `json:"columns"`
	Rows    [][]any          `json:"rows"`
	// HasMore reports whether rows follow this page: the extra row the page
	// statement fetched came back, or the byte cap cut the page short.
	HasMore bool `json:"has_more"`
	// Total is the table's size under the request's filters, -1 with
	// CountNone or when the estimate is unknown. TotalIsEstimate marks a
	// planner estimate (CountEstimated); TotalAtLeast marks an exact count
	// that stopped at ExactCountCap, so the size is at least Total.
	Total           int64 `json:"total"`
	TotalIsEstimate bool  `json:"total_is_estimate,omitempty"`
	TotalAtLeast    bool  `json:"total_at_least,omitempty"`
	// Truncated is true when dbquery's byte cap, not the page size, ended the
	// page; CellsTruncated counts the page's cells cut to
	// dbquery.MaxCellBytes, and TruncatedCells says which: nil when none was,
	// otherwise one entry per row, aligned with Rows, listing the 0-based
	// column indexes of that row's cut cells (see
	// [dbquery.Result.TruncatedCells]).
	Truncated      bool    `json:"truncated,omitempty"`
	CellsTruncated int     `json:"cells_truncated,omitempty"`
	TruncatedCells [][]int `json:"truncated_cells,omitempty"`
	DurationMS     int64   `json:"duration_ms"`
}

// Run executes c through [dbquery.Run]: each statement in a READ ONLY
// transaction under opts' timeouts and byte cap, past dbquery's guard, and
// reported to opts.Auditor. A page is one statement and a count is a second
// one, so an Auditor sees one Event per statement; Event.SQL tells them apart.
//
// opts.MaxRows is ignored: the page statement's row cap is c.Limit+1, and a
// count returns one row. A statement's failure is a *[dbquery.Error].
func Run(ctx context.Context, db dbquery.Beginner, c *Compiled, opts dbquery.Options) (*Page, error) {
	if c == nil || c.page.SQL == "" {
		return nil, fmt.Errorf("%w: Run needs a statement from Compile", ErrInvalidRequest)
	}
	popts := opts
	popts.MaxRows = c.Limit + 1
	popts.Args = c.page.Args
	res, err := dbquery.Run(ctx, db, c.page.SQL, popts)
	if err != nil {
		return nil, err
	}
	p := &Page{
		Columns:        res.Columns,
		Rows:           res.Rows,
		Total:          -1,
		Truncated:      res.TruncatedBy == "bytes",
		CellsTruncated: res.CellsTruncated,
		TruncatedCells: res.TruncatedCells,
		DurationMS:     res.DurationMS,
	}
	if len(p.Rows) > c.Limit {
		p.Rows, p.HasMore = p.Rows[:c.Limit], true
		if p.TruncatedCells != nil {
			// The extra row is not shown: neither are its cut cells.
			p.TruncatedCells = p.TruncatedCells[:c.Limit]
			p.CellsTruncated = 0
			for _, cut := range p.TruncatedCells {
				p.CellsTruncated += len(cut)
			}
			if p.CellsTruncated == 0 {
				p.TruncatedCells = nil
			}
		}
	}
	if p.Truncated {
		p.HasMore = true
	}

	switch {
	case c.Count == CountEstimated && c.count == nil:
		p.Total, p.TotalIsEstimate = c.estimate, true
	case c.count != nil:
		copts := opts
		copts.MaxRows = 1
		copts.Args = c.count.Args
		cres, err := dbquery.Run(ctx, db, c.count.SQL, copts)
		if err != nil {
			return nil, err
		}
		p.DurationMS += cres.DurationMS
		n, err := readCount(c.Count, cres)
		if err != nil {
			return nil, err
		}
		p.Total = n
		switch c.Count {
		case CountEstimated:
			p.TotalIsEstimate = true
		case CountExact:
			if n > ExactCountCap {
				p.Total, p.TotalAtLeast = ExactCountCap, true
			}
		}
	}
	return p, nil
}

// readCount reads the single cell of a count statement: count(*) for an
// exact count, an EXPLAIN (FORMAT JSON) plan for an estimate.
func readCount(mode CountMode, res *dbquery.Result) (int64, error) {
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return 0, badCount("returned %d rows", len(res.Rows))
	}
	cell := res.Rows[0][0]
	if mode == CountExact {
		num, ok := cell.(json.Number)
		if !ok {
			return 0, badCount("count(*) returned %T", cell)
		}
		n, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil {
			return 0, badCount("count(*) returned %q", num)
		}
		return n, nil
	}
	var raw []byte
	switch v := cell.(type) {
	case json.RawMessage:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return 0, badCount("EXPLAIN returned %T", cell)
	}
	var plan []struct {
		Plan struct {
			Rows float64 `json:"Plan Rows"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
		return 0, badCount("EXPLAIN returned an unexpected plan")
	}
	return int64(math.Round(plan[0].Plan.Rows)), nil
}

func badCount(format string, a ...any) error {
	return &dbquery.Error{Kind: dbquery.KindSQL, Reason: "count", Message: "the count statement " + fmt.Sprintf(format, a...)}
}
