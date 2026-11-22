package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// TestUndeclaredGroupByWarnings pins obs 03279cf8: `--group-by rpc.method` on
// rpc.server.duration came back as one group with an empty rpc.method and no
// hint that the interceptor records the RPC as `procedure`.
func TestUndeclaredGroupByWarnings(t *testing.T) {
	t.Parallel()
	duration := []obsquery.PanelSeries{{Label: "A", Field: "rpc.server.duration"}}
	empty := func(key string) []obsbackend.MetricSeries {
		return []obsbackend.MetricSeries{{Label: "A", Labels: map[string]string{key: ""}}}
	}
	cases := map[string]struct {
		series  []obsquery.PanelSeries
		groupBy string
		result  []obsbackend.MetricSeries
		want    string // "" = no warning
	}{
		"undeclared key, one empty group": {duration, "rpc.method", empty("rpc.method"), `rpc.server.duration records no "rpc.method" attribute`},
		"declared key":                    {duration, "procedure", empty("procedure"), ""},
		"resource key":                    {duration, "service.name", empty("service.name"), ""},
		"undeclared key but values came back": {duration, "rpc.method",
			[]obsbackend.MetricSeries{{Label: "A", Labels: map[string]string{"rpc.method": "PlaceOrder"}}}, ""},
		"project metric is not judged": {[]obsquery.PanelSeries{{Label: "A", Field: "orders_created"}}, "rpc.method", empty("rpc.method"), ""},
		"no data is not judged":        {duration, "rpc.method", nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := undeclaredGroupByWarnings(obsquery.Panel{Series: tc.series, GroupBy: []string{tc.groupBy}}, tc.result, nil)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("want no warning, got %q", got)
				}
				return
			}
			if len(got) != 1 || !strings.HasPrefix(got[0], warnGroupByUndeclared+": ") || !strings.Contains(got[0], tc.want) || !strings.Contains(got[0], "its attributes: procedure") {
				t.Fatalf("want one %s warning containing %q and naming procedure, got %q", warnGroupByUndeclared, tc.want, got)
			}
		})
	}
}

func TestMetricsQueryWarnsForEmptyGroupsInEitherRange(t *testing.T) {
	group := func(value string) []obsbackend.MetricSeries {
		return []obsbackend.MetricSeries{{Label: "A", Labels: map[string]string{"rpc.method": value}}}
	}
	for _, tc := range []struct {
		name, metric     string
		primary, compare []obsbackend.MetricSeries
		want             int
	}{
		{"comparison only", "rpc.server.duration", group("List"), group(""), 1},
		{"primary only", "rpc.server.duration", group(""), group("List"), 1},
		{"comparison with no primary data", "rpc.server.duration", nil, group(""), 1},
		{"both ranges deduplicate", "rpc.server.duration", group(""), group(""), 1},
		{"comparison has no data", "rpc.server.duration", group("List"), nil, 0},
		{"custom metric", "orders_created", group("List"), group(""), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := groupByCompareBackend{result: obsbackend.MetricsQueryResult{
				Series: tc.primary, Compare: &obsbackend.MetricsCompare{Series: tc.compare},
			}}
			out, _, err := mxRun(t, obsDeps{
				Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
				Local: func(string) obsbackend.Backend { return b },
			}, "metrics", "query", "dev", tc.metric, "--agg", "avg", "--group-by", "rpc.method", "--compare", "24h", "--json")
			if err != nil {
				t.Fatal(err)
			}
			warnings, _ := decode(t, out)["warnings"].([]any)
			if len(warnings) != tc.want {
				t.Fatalf("got warnings %v, want %d", warnings, tc.want)
			}
			if tc.want > 0 && !strings.HasPrefix(warnings[0].(string), warnGroupByUndeclared+": ") {
				t.Fatalf("unexpected warning: %v", warnings[0])
			}
		})
	}
}

type groupByCompareBackend struct {
	obsbackend.Backend
	result obsbackend.MetricsQueryResult
}

func (b groupByCompareBackend) Info() obsbackend.Info {
	return obsbackend.Info{Env: "dev", Backend: obsbackend.BackendLocal}
}

func (b groupByCompareBackend) MetricsQuery(context.Context, obsbackend.MetricsQueryRequest) (obsbackend.MetricsQueryResult, error) {
	return b.result, nil
}
