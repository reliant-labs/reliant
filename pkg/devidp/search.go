package devidp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// searchPageSize is how many results one search page asks the issuer for.
// The issuer may answer fewer (its own maximum); searchAll pages on by the
// count it reports, not by this number. A var so a test can shrink it.
var searchPageSize = 100

// searchAll reads EVERY result of an issuer search, not just the first page.
// Each page's results are the array under resultKey; the issuer's
// details.totalResult says how many there are in all. With paged, the
// request carries a {offset, limit} query and searchAll reads pages until it
// holds totalResult; without it (a search with no paging, such as
// passkeys), the one answer must already hold them all.
//
// It fails closed: a revocation built on a partial list would report success
// while credentials or sessions remained, so an answer that cannot prove it
// is complete — results with no total, or a page that ends short of the
// total — is an error, never a shorter list.
func searchAll[T any](ctx context.Context, c *Client, path string, body map[string]any, resultKey string, paged bool) ([]T, error) {
	all := []T{}
	for {
		req := map[string]any{}
		for k, v := range body {
			req[k] = v
		}
		if paged {
			req["query"] = map[string]any{"offset": strconv.Itoa(len(all)), "limit": searchPageSize, "asc": true}
		}
		raw, err := c.postJSON(ctx, path, req)
		if err != nil {
			return nil, err
		}
		page, total, hasTotal, err := decodeSearchPage[T](raw, resultKey)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if !hasTotal {
			// protojson omits a zero total, so an empty answer with none
			// is an empty result; results with none prove nothing.
			if len(all) == 0 {
				return all, nil
			}
			return nil, fmt.Errorf("the issuer returned %d results with no total: cannot tell whether that is all of them", len(all))
		}
		if uint64(len(all)) >= total {
			return all, nil
		}
		if !paged || len(page) == 0 {
			return nil, fmt.Errorf("the issuer reports %d results but returned %d", total, len(all))
		}
	}
}

// decodeSearchPage reads one search answer: its results and the total the
// issuer reports (a uint64, which protojson writes as a string).
func decodeSearchPage[T any](raw []byte, resultKey string) (page []T, total uint64, hasTotal bool, err error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, false, fmt.Errorf("decode search results: %w", err)
	}
	if r, ok := out[resultKey]; ok {
		if err := json.Unmarshal(r, &page); err != nil {
			return nil, 0, false, fmt.Errorf("decode search results: %w", err)
		}
	}
	var details struct {
		TotalResult json.RawMessage `json:"totalResult"`
	}
	if d, ok := out["details"]; ok {
		if err := json.Unmarshal(d, &details); err != nil {
			return nil, 0, false, fmt.Errorf("decode search details: %w", err)
		}
	}
	if len(details.TotalResult) == 0 || string(details.TotalResult) == "null" {
		return page, 0, false, nil
	}
	s := strings.Trim(string(details.TotalResult), `"`)
	total, err = strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil, 0, false, fmt.Errorf("decode search total %q: %w", s, err)
	}
	return page, total, true, nil
}
