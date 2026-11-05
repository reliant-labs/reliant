package dashspec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const groupBase = `{"id":"ops","title":"Ops",%s"panels":[{"id":"p","title":"P","kind":"number","signal":"traces","series":[{"label":"n","agg":"count"}]}]}`

func TestGroupTagsAndReplacesRoundTrip(t *testing.T) {
	src := strings.Replace(groupBase, "%s", `"group":"jobs","tags":["slo","kind.cron"],"replaces":["process-old","process-older"],`, 1)
	d, err := ParseDashboard("g", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if d.Group != GroupJobs || !reflect.DeepEqual(d.Tags, []string{"slo", "kind.cron"}) || !reflect.DeepEqual(d.Replaces, []string{"process-old", "process-older"}) {
		t.Fatalf("lost on parse: %+v", d)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseDashboard("g", out)
	if err != nil || !reflect.DeepEqual(back, d) {
		t.Fatalf("round trip changed the dashboard: %v\n%+v\n%+v", err, d, back)
	}
}

func TestGroupIsOptionalAndAbsentWhenUnset(t *testing.T) {
	src := strings.Replace(groupBase, "%s", "", 1)
	d, err := ParseDashboard("g", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if d.Group != "" || GroupOf(d) != GroupCustom {
		t.Fatalf("group = %q, GroupOf = %q; want unset and custom", d.Group, GroupOf(d))
	}
	out, _ := json.Marshal(d)
	for _, k := range []string{`"group"`, `"tags"`, `"replaces"`} {
		if strings.Contains(string(out), k) {
			t.Errorf("%s serialized for a dashboard that sets none: %s", k, out)
		}
	}
	if GroupOf(Dashboard{Group: "someday"}) != GroupCustom {
		t.Error("an unknown group must read as custom")
	}
}

func TestGroupTagsAndReplacesAreValidated(t *testing.T) {
	cases := map[string]string{
		`"group":"nope",`:   `"nope" is not one of`,
		`"group":7,`:        "must be a string",
		`"tags":["Upper"],`: "not a valid tag",
		`"tags":["a","a"],`: "repeats an earlier tag",
		`"tags":"slo",`:     "must be an array",
		`"tags":["a","b","c","d","e","f","g","h","i"],`: "over the 8 allowed",
		`"tags":[3],`:           "must be a string",
		`"replaces":["ops"],`:   "cannot replace itself",
		`"replaces":["a b"],`:   "not a valid dashboard id",
		`"replaces":["x","x"],`: "repeats an earlier id",
	}
	for field, want := range cases {
		_, err := ParseDashboard("g", []byte(strings.Replace(groupBase, "%s", field, 1)))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to contain %q", field, err, want)
		}
	}
}

func TestEveryGroupIsAccepted(t *testing.T) {
	for _, g := range Groups {
		if _, err := ParseDashboard("g", []byte(strings.Replace(groupBase, "%s", `"group":"`+string(g)+`",`, 1))); err != nil {
			t.Errorf("group %q refused: %v", g, err)
		}
	}
}
