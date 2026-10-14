package wfcel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The data logic the Autopilot prototype kept in a bash helper
// (research/autopilot/bin/autopilot), written as workflow CEL. The expressions
// here are the ones documented in reference/cel-expressions and the
// workflow-builder skill; a change to one belongs in all three.

func helperCtx() *NodeResolutionContext {
	return &NodeResolutionContext{
		Inputs: map[string]interface{}{
			"labels":  []interface{}{"agent:ready", "size:S", "risk:low"},
			"repo":    "reliant-labs/forge",
			"num":     "743",
			"run":     "run-1.2:ok",
			"fp":      "469e6b810b31bcc6",
			"adds":    int64(40),
			"dels":    int64(30),
			"expires": "2026-10-14T03:10:45Z",
			"now":     "2026-10-14T03:00:00Z",
			"ttl":     "45m",
			"body":    "<!-- autopilot -->\nReviewed `abc`.",
			"stdout":  "{\"status\":\"ready\",\"worktree\":\"/w/forge-743\",\"id\":225976313}",
			"leases": []interface{}{
				map[string]interface{}{"id": int64(9), "created_at": "2026-10-14T02:26:01Z", "state": "held", "run": "b", "expires": "2026-10-14T03:10:45Z"},
				map[string]interface{}{"id": int64(7), "created_at": "2026-10-14T02:26:01Z", "state": "held", "run": "a", "expires": "2026-10-14T02:50:00Z"},
				map[string]interface{}{"id": int64(3), "created_at": "2026-10-14T02:25:58Z", "state": "released", "run": "z", "expires": "2026-10-14T02:30:00Z"},
				map[string]interface{}{"id": int64(8), "created_at": "2026-10-14T02:26:00Z", "state": "held", "run": "c", "expires": "2026-10-14T03:30:00Z"},
			},
		},
	}
}

func TestHelperLogicInCEL(t *testing.T) {
	t.Parallel()
	const allowedRepos = `['reliant-labs/forge', 'reliant-labs/control-plane', 'reliant-labs/reliant']`

	cases := []struct {
		name string
		expr string
		want any
	}{
		// has_label
		{"has_label", `'agent:ready' in inputs.labels`, true},
		{"has_label absent", `'agent:blocked' in inputs.labels`, false},
		{"has any label", `['agent:authored', 'agent:respond'].exists(l, l in inputs.labels)`, false},

		// marker detection (the <!-- autopilot --> mark on text the bot wrote)
		{"marker present", `inputs.body.contains('<!-- autopilot')`, true},
		{"marker as a prefix", `inputs.body.startsWith('<!-- autopilot')`, true},
		{"marker absent", `'a human wrote this'.contains('<!-- autopilot')`, false},

		// valid_repo / valid_int / valid_run / fingerprint validation
		{"valid_repo", `` + allowedRepos + `.exists(r, r == inputs.repo)`, true},
		{"valid_repo refused", `` + allowedRepos + `.exists(r, r == 'evil/repo')`, false},
		{"valid_int", `inputs.num.matches('^[1-9][0-9]{0,9}$')`, true},
		{"valid_int refuses 0 and text", `['0', '12a', ''].exists(s, s.matches('^[1-9][0-9]{0,9}$'))`, false},
		{"valid_run", `inputs.run.matches('^[A-Za-z0-9._:-]{1,128}$')`, true},
		{"valid_run refuses a shell metacharacter", `'a;rm -rf'.matches('^[A-Za-z0-9._:-]{1,128}$')`, false},
		{"valid fingerprint", `inputs.fp.matches('^[0-9a-fA-F]{6,64}$')`, true},

		// time arithmetic: lease expiry
		{"lease is live", `timestamp(inputs.expires) > timestamp(inputs.now)`, true},
		{"lease expired", `timestamp(inputs.leases[1].expires) <= timestamp(inputs.now)`, true},
		{"lease expiry = now + ttl", `string(timestamp(inputs.now) + duration(inputs.ttl))`, "2026-10-14T03:45:00Z"},
		{"heartbeat renews 15m", `string(timestamp(inputs.now) + duration('15m'))`, "2026-10-14T03:15:00Z"},
		{"minutes left", `int((timestamp(inputs.expires) - timestamp(inputs.now)).getMinutes())`, int64(10)},
		{"lease expires within 15 minutes", `timestamp(inputs.expires) - timestamp(inputs.now) < duration('15m')`, true},

		// parsing a node's JSON stdout
		{"parse stdout", `parseJson(inputs.stdout).status`, "ready"},
		{"stdout status, tolerant", `json.decode(inputs.stdout).?status.orValue('error')`, "ready"},
		{"stdout that is not json", `json.decode('Traceback (most recent call last)').?status.orValue('error')`, "error"},
		{"empty stdout", `json.decode('').?status.orValue('error')`, "error"},
		{"stdout starts with a brace", `inputs.stdout.startsWith('{') && parseJson(inputs.stdout).status == 'ready'`, true},
		{"json integers stay integers", `string(parseJson(inputs.stdout).id)`, "225976313"},

		// size_label from additions plus deletions
		{"size_label M", `inputs.adds + inputs.dels < 60 ? 'size:S' : (inputs.adds + inputs.dels < 400 ? 'size:M' : 'size:L')`, "size:M"},

		// map_repo from labels
		{"map_repo explicit", `['forge', 'control-plane', 'reliant'].exists(r, r == 'forge') ? 'reliant-labs/forge' : ''`, "reliant-labs/forge"},
		{"map_repo by service", `
			cel.bind(svc, 'Reliant-API-Server'.lowerAscii(),
			  svc.matches('^(reliant-)?(api-server|temporal-worker|daemon-gateway|daemon)$') || svc == 'reliant-web' || svc == 'reliant'
			    ? 'reliant-labs/reliant'
			    : (svc.matches('^forge($|[-_])') ? 'reliant-labs/forge' : 'reliant-labs/control-plane'))`, "reliant-labs/reliant"},
		{"map_repo default", `'billing'.matches('^forge($|[-_])') ? 'reliant-labs/forge' : 'reliant-labs/control-plane'`, "reliant-labs/control-plane"},

		// picking the earliest lease: earliest created_at among held leases, then lowest id
		{"earliest held lease", `
			cel.bind(held, inputs.leases.filter(l, l.state == 'held'),
			  cel.bind(first, held.map(l, l.created_at).sort()[0],
			    held.filter(l, l.created_at == first).sortBy(l, l.id)[0].run))`, "c"},
		{"earliest expiry across held leases", `inputs.leases.filter(l, l.state == 'held').map(l, timestamp(l.expires)).sort()[0] == timestamp('2026-10-14T02:50:00Z')`, true},
		{"is there another live lease", `inputs.leases.exists(l, l.state == 'held' && l.run != 'a' && timestamp(l.expires) > timestamp(inputs.now))`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := EvaluateValue(tc.expr, helperCtx())
			require.NoError(t, err, tc.expr)
			assert.Equal(t, tc.want, got, tc.expr)
		})
	}
}

func TestExtensionsAreEnabled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		expr string
		want any
	}{
		{"strings", `'a,b'.split(',').join('-')`, "a-b"},
		{"strings lowerAscii", `'ABC'.lowerAscii()`, "abc"},
		{"lists sort", `[3, 1, 2].sort()`, []interface{}{int64(1), int64(2), int64(3)}},
		{"lists sortBy", `[{'n': 2}, {'n': 1}].sortBy(e, e.n)[0].n`, int64(1)},
		{"lists distinct", `[1, 1, 2].distinct()`, []interface{}{int64(1), int64(2)}},
		{"lists flatten", `[[1], [2, 3]].flatten()`, []interface{}{int64(1), int64(2), int64(3)}},
		{"lists.range", `lists.range(3)`, []interface{}{int64(0), int64(1), int64(2)}},
		{"sets.contains", `sets.contains([1, 2, 3], [2, 3])`, true},
		{"sets.intersects", `sets.intersects(['a'], ['b'])`, false},
		{"math.greatest", `math.greatest(1, 5, 3)`, int64(5)},
		{"math.least", `math.least([4, 2, 9])`, int64(2)},
		{"base64.encode", `base64.encode(b'hi')`, "aGk="},
		{"base64.decode", `string(base64.decode('aGk='))`, "hi"},
		{"optional chaining", `{'a': {'b': 1}}.?a.?b.orValue(0)`, int64(1)},
		{"optional absent", `{'a': 'x'}.?b.orValue('none')`, "none"},
		{"cel.bind", `cel.bind(x, 2, x * x)`, int64(4)},
		{"regex.extract", `regex.extract('run-42-x', 'run-(\\d+)').orValue('')`, "42"},
		{"regex.replace", `regex.replace('a-b-c', '-', '_')`, "a_b_c"},
		{"two-variable comprehension", `{'a': 1, 'b': 2}.all(k, v, v > 0)`, true},
		{"timestamp arithmetic", `string(timestamp('2026-01-01T00:00:00Z') + duration('36h'))`, "2026-01-02T12:00:00Z"},
		{"timestamp accessor", `timestamp('2026-10-14T02:25:58Z').getHours()`, int64(2)},
		{"timestamp from epoch", `string(timestamp(1791944760))`, "2026-10-14T02:26:00Z"},
		{"duration accessor", `duration('90s').getSeconds()`, int64(90)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := EvaluateValue(tc.expr, &NodeResolutionContext{})
			require.NoError(t, err, tc.expr)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestJSONDecode(t *testing.T) {
	t.Parallel()
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"s": `{"a": [1, 2.5, "x"], "ok": true}`}}

	got, err := EvaluateValue(`json.decode(inputs.s).hasValue()`, ctx)
	require.NoError(t, err)
	assert.Equal(t, true, got)

	got, err = EvaluateValue(`json.decode(inputs.s).value().a[1]`, ctx)
	require.NoError(t, err)
	assert.Equal(t, 2.5, got)

	for _, bad := range []string{``, `   `, `not json`, `{"a":`, `{"a":1} trailing`, `{"a":1}{"b":2}`} {
		got, err = EvaluateValue(`json.decode(inputs.t).hasValue()`, &NodeResolutionContext{Inputs: map[string]interface{}{"t": bad}})
		require.NoError(t, err, "%q", bad)
		assert.Equal(t, false, got, "%q is not one JSON value", bad)
	}

	// parseJson stays strict: its failure is the step's failure.
	_, err = EvaluateValue(`parseJson(inputs.t)`, &NodeResolutionContext{Inputs: map[string]interface{}{"t": "not json"}})
	require.Error(t, err)
	_, err = EvaluateValue(`parseJson(inputs.t)`, &NodeResolutionContext{Inputs: map[string]interface{}{"t": `{"a":1} x`}})
	require.Error(t, err, "trailing data after the value is an error")

	// Bounded input.
	big := make([]byte, MaxJSONDecodeBytes+1)
	for i := range big {
		big[i] = ' '
	}
	_, err = EvaluateValue(`parseJson(inputs.t)`, &NodeResolutionContext{Inputs: map[string]interface{}{"t": string(big)}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

func TestCostLimitStillApplies(t *testing.T) {
	t.Parallel()
	ctx := &NodeResolutionContext{}

	// 100k x 100k is 10^10 steps: stopped by the cost limit, not by running out of time.
	_, err := EvaluateValue(`lists.range(100000).map(a, lists.range(100000).size()).size()`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost limit exceeded")
	assert.Contains(t, err.Error(), "narrow the data")

	// lists.range is capped before it allocates.
	_, err = EvaluateValue(`lists.range(1000000000).size()`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")

	// A large but sane expression is fine.
	got, err := EvaluateValue(`lists.range(5000).map(n, n * 2).filter(n, n % 3 == 0).size()`, ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1667), got)

	// The limit applies through templates too.
	_, err = EvaluateTemplate(`x {{ lists.range(100000).map(a, lists.range(100000).size()).size() }}`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost limit exceeded")
}

func TestJSONDecodeCostScalesWithInput(t *testing.T) {
	t.Parallel()
	// 2000 decodes of 64 KiB: free under a flat per-call charge, over the limit when charged by size.
	body := `{"k":"` + strings.Repeat("a", 64<<10) + `"}`
	ctx := &NodeResolutionContext{Inputs: map[string]interface{}{"b": body}}
	_, err := EvaluateValue(`lists.range(2000).map(i, json.decode(inputs.b).hasValue()).size()`, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cost limit exceeded")

	got, err := EvaluateValue(`json.decode(inputs.b).hasValue()`, ctx)
	require.NoError(t, err)
	assert.Equal(t, true, got, "one decode of a large body is fine")
}
