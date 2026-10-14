package wfcel

import (
	"testing"
)

func TestToJsonSerializesCELValues(t *testing.T) {
	nodes := map[string]interface{}{
		"files": map[string]interface{}{
			"names": []interface{}{"a.go", "b.go", "c.txt"},
			"count": int64(3),
			"meta":  map[string]interface{}{"ok": true, "ratio": 0.5, "none": nil},
		},
		"rows": []interface{}{
			map[string]interface{}{"name": "x", "n": int64(1)},
			map[string]interface{}{"name": "y", "n": int64(2)},
		},
	}
	tests := []struct {
		name string
		expr string
		want string
	}{
		{"string list literal", `toJson(['a','b'])`, `["a","b"]`},
		{"empty list", `toJson([])`, `[]`},
		{"empty map", `toJson({})`, `{}`},
		{"map with nested list", `toJson({'k': [1,2]})`, `{"k":[1,2]}`},
		{"nested structures", `toJson({'a': {'b': [{'c': 'd'}, []]}, 'e': [[1], [2.5]]})`, `{"a":{"b":[{"c":"d"},[]]},"e":[[1],[2.5]]}`},
		{"null inside list", `toJson([1, null, 'x'])`, `[1,null,"x"]`},
		{"null inside map", `toJson({'a': null})`, `{"a":null}`},
		{"bare null", `toJson(null)`, `null`},
		{"int stays int", `toJson([1, 2])`, `[1,2]`},
		{"large int is exact", `toJson([9007199254740993])`, `[9007199254740993]`},
		{"uint", `toJson([3u])`, `[3]`},
		{"double", `toJson([1.5, 2.0])`, `[1.5,2]`},
		{"bool", `toJson([true, false])`, `[true,false]`},
		{"scalar string", `toJson('hi "there"')`, `"hi \"there\""`},
		{"scalar int", `toJson(7)`, `7`},
		{"bytes are base64", `toJson(b'abc')`, `"YWJj"`},
		{"map() result", `toJson([1,2,3].map(x, x * 2))`, `[2,4,6]`},
		{"filter() result", `toJson(['a','bb','cc'].filter(s, size(s) > 1))`, `["bb","cc"]`},
		{"map() producing maps", `toJson([1,2].map(x, {'n': x}))`, `[{"n":1},{"n":2}]`},
		{"member form", `['a'].toJson()`, `["a"]`},
		{"node list passthrough", `toJson(nodes.files.names)`, `["a.go","b.go","c.txt"]`},
		{"node map passthrough", `toJson(nodes.files.meta)`, `{"none":null,"ok":true,"ratio":0.5}`},
		{"filter over node list", `toJson(nodes.files.names.filter(n, n.endsWith('.go')))`, `["a.go","b.go"]`},
		{"map over node list", `toJson(nodes.files.names.map(n, n.upperAscii()))`, `["A.GO","B.GO","C.TXT"]`},
		{"map over node rows", `toJson(nodes.rows.map(r, r.name))`, `["x","y"]`},
		{"filter over node rows", `toJson(nodes.rows.filter(r, r.n > 1))`, `[{"n":2,"name":"y"}]`},
		{"mixed cel and node values", `toJson({'names': nodes.files.names, 'count': nodes.files.count, 'tags': ['t']})`, `{"count":3,"names":["a.go","b.go","c.txt"],"tags":["t"]}`},
		{"merge result", `toJson(merge({'a': 1}, {'b': [2]}))`, `{"a":1,"b":[2]}`},
		{"sorted list", `toJson([3,1,2].sort())`, `[1,2,3]`},
		{"split result", `toJson('a,b'.split(','))`, `["a","b"]`},
		{"duration", `toJson([duration('90s')])`, `["90s"]`},
		{"optional some", `toJson([optional.of(1).orValue(0)])`, `[1]`},
		{"timestamp", `toJson(timestamp('2024-01-02T03:04:05Z'))`, `"2024-01-02T03:04:05Z"`},
		{"parseJson roundtrip", `toJson(parseJson('{"a":[1,{"b":null}]}'))`, `{"a":[1,{"b":null}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateValue(tc.expr, &EdgeEvalContext{Nodes: nodes, Inputs: map[string]interface{}{}})
			if err != nil {
				t.Fatalf("%s: %v", tc.expr, err)
			}
			if got != tc.want {
				t.Fatalf("%s\n got: %v\nwant: %s", tc.expr, got, tc.want)
			}
		})
	}
}

func TestToJsonErrors(t *testing.T) {
	for _, expr := range []string{
		`toJson({1: 'a'})`,    // non-string map key
		`toJson([1.0 / 0.0])`, // +Inf has no JSON form
	} {
		if _, err := EvaluateValue(expr, &EdgeEvalContext{Inputs: map[string]interface{}{}}); err == nil {
			t.Errorf("%s: expected an error", expr)
		}
	}
}
