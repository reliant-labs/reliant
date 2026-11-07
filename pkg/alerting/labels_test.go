// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"errors"
	"strings"
	"testing"
)

func testRule() Rule {
	return Rule{
		RuleKey:  RuleKey{Owner: "org-a", Env: "prod", Name: "slow"},
		Identity: map[string]string{"reliant_org_id": "org-a", "reliant_env_id": "prod"},
	}
}

func TestIdentityLabelsCannotBeOverridden(t *testing.T) {
	r := testRule()
	r.Labels = map[string]string{
		"reliant_org_id": "org-b", "RELIANT_ENV_ID": "evil", "Reliant_Extra": "x",
		"alertname": "spoofed", "team": "pay",
	}
	ss, err := buildSeries(r, []Row{{
		"service": "api", "reliant_org_id": "org-b", "Reliant_Org_Id": "org-b", "value": 3.0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	l := ss[0].labels
	if l["reliant_org_id"] != "org-a" || l["reliant_env_id"] != "prod" {
		t.Fatalf("identity overridden: %v", l)
	}
	if l["alertname"] != "slow" {
		t.Fatalf("alertname = %q", l["alertname"])
	}
	for k := range l {
		if isReserved(k) && k != "reliant_org_id" && k != "reliant_env_id" {
			t.Fatalf("user-supplied reserved label survived: %q", k)
		}
	}
	if l["team"] != "pay" || l["service"] != "api" {
		t.Fatalf("ordinary labels lost: %v", l)
	}
}

func TestValueColumnIsTheSampleNotALabel(t *testing.T) {
	ss, err := buildSeries(testRule(), []Row{{"service": "api", "value": uint64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].value != 7 {
		t.Fatalf("value = %v", ss[0].value)
	}
	if _, ok := ss[0].labels["value"]; ok {
		t.Fatal("value must not be a label")
	}
}

func TestMissingValueColumnDefaultsToOne(t *testing.T) {
	ss, err := buildSeries(testRule(), []Row{{"service": "api"}})
	if err != nil || ss[0].value != 1 {
		t.Fatalf("got %v, %v", ss, err)
	}
}

func TestBadValueIsAnError(t *testing.T) {
	for _, v := range []any{nil, "abc", nanValue()} {
		if _, err := buildSeries(testRule(), []Row{{"value": v}}); !errors.Is(err, ErrBadResult) {
			t.Fatalf("value %v: err = %v", v, err)
		}
	}
}

func TestDuplicateLabelSetsAreAnError(t *testing.T) {
	_, err := buildSeries(testRule(), []Row{{"service": "a", "value": 1.0}, {"service": "a", "value": 2.0}})
	if !errors.Is(err, ErrBadResult) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityNamesMustBeValid(t *testing.T) {
	r := testRule()
	r.Identity = map[string]string{"bad name": "x"}
	if _, err := buildSeries(r, nil); !errors.Is(err, ErrBadResult) {
		t.Fatalf("err = %v", err)
	}
}

func TestLabelNamesAreRepairedAndValuesBounded(t *testing.T) {
	ss, err := buildSeries(testRule(), []Row{{
		"http.status-code": 500, "9lives": "x", "___": "dropped", "big": strings.Repeat("é", 2000), "empty": "", "value": 1.0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	l := ss[0].labels
	if l["http_status_code"] != "500" || l["_9lives"] != "x" {
		t.Fatalf("labels = %v", l)
	}
	if _, ok := l["___"]; ok {
		t.Fatal("an all-underscore name must be dropped")
	}
	if _, ok := l["empty"]; ok {
		t.Fatal("empty values must be dropped")
	}
	if len(l["big"]) > maxLabelValueBytes || strings.ContainsRune(l["big"], '\uFFFD') {
		t.Fatalf("big value not cut cleanly: %d bytes", len(l["big"]))
	}
}

func TestFingerprintIsStableAndScoped(t *testing.T) {
	a := fingerprint(RuleKey{"t", "e", "r"}, map[string]string{"x": "1", "y": "2"})
	if a != fingerprint(RuleKey{"t", "e", "r"}, map[string]string{"y": "2", "x": "1"}) {
		t.Fatal("fingerprint depends on map order")
	}
	if a == fingerprint(RuleKey{"t2", "e", "r"}, map[string]string{"x": "1", "y": "2"}) {
		t.Fatal("fingerprint must differ across owners")
	}
	if a == fingerprint(RuleKey{"t", "e", "r"}, map[string]string{"x": "1", "y": "3"}) {
		t.Fatal("fingerprint must differ across values")
	}
}

func TestAnnotationTemplates(t *testing.T) {
	r := testRule()
	r.Annotations = map[string]string{"summary": "{{ $labels.service }} p95 is {{$value}}ms {{ $labels.nope }}", "plain": "x"}
	ss, err := buildSeries(r, []Row{{"service": "api", "value": 812.5}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ss[0].annotations["summary"]; got != "api p95 is 812.5ms " {
		t.Fatalf("summary = %q", got)
	}
}

func nanValue() float64 { var z float64; return z / z }
