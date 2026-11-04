package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func fakeProfileRunner(services, rows string) (profileRunner, *[]string) {
	var seen []string
	return func(_ context.Context, sql string, params map[string]string) ([]byte, error) {
		seen = append(seen, sql)
		if strings.Contains(sql, "GROUP BY service") {
			return []byte(services), nil
		}
		return []byte(rows), nil
	}, &seen
}

const cliRows = `{"frames":["main.main","main.work"],"unit":"nanoseconds","value":"500"}` + "\n"

func TestObserveProfileFoldedForTheOnlyProfiledService(t *testing.T) {
	run, _ := fakeProfileRunner(`{"service":"api","rows":"9"}`+"\n", cliRows)
	var out, errb bytes.Buffer
	err := runObsProfile(context.Background(), &out, &errb, obsProfileOpts{sampleType: "cpu", since: time.Hour, format: "folded"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "main.main;main.work 500\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestObserveProfileAsksWhichServiceWhenSeveralSentProfiles(t *testing.T) {
	run, _ := fakeProfileRunner(`{"service":"api","rows":"9"}`+"\n"+`{"service":"worker","rows":"3"}`+"\n", cliRows)
	err := runObsProfile(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, obsProfileOpts{sampleType: "cpu", since: time.Hour, format: "folded"}, run)
	if err == nil || !strings.Contains(err.Error(), "api, worker") {
		t.Errorf("want an error naming both services, got %v", err)
	}
}

func TestObserveProfileSaysWhyWhenThereIsNothing(t *testing.T) {
	run, _ := fakeProfileRunner("", "")
	err := runObsProfile(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, obsProfileOpts{sampleType: "cpu", since: time.Hour, format: "folded"}, run)
	if err == nil || !strings.Contains(err.Error(), "no service has sent profiles") {
		t.Errorf("got %v", err)
	}
	err = runObsProfile(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, obsProfileOpts{service: "api", sampleType: "cpu", since: time.Hour, format: "folded"}, run)
	if err == nil || !strings.Contains(err.Error(), "no profile samples") {
		t.Errorf("got %v", err)
	}
}

func TestObserveProfileSQLFlagRunsNothing(t *testing.T) {
	run := func(context.Context, string, map[string]string) ([]byte, error) {
		return nil, errors.New("must not run")
	}
	var out bytes.Buffer
	err := runObsProfile(context.Background(), &out, &bytes.Buffer{}, obsProfileOpts{service: "api", sampleType: "cpu", since: time.Hour, format: "pprof", printSQL: true}, run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "profile_samples_1m") || !strings.Contains(out.String(), `{svc} = "api"`) {
		t.Errorf("--sql output = %s", out.String())
	}
}

func TestObserveProfileRejectsBadInput(t *testing.T) {
	run, _ := fakeProfileRunner("", "")
	for name, o := range map[string]obsProfileOpts{
		"bad format":         {service: "a", sampleType: "cpu", since: time.Hour, format: "svg"},
		"span without trace": {service: "a", sampleType: "cpu", since: time.Hour, format: "folded", span: "ab"},
		"empty range":        {service: "a", sampleType: "cpu", since: 0, format: "folded"},
		"bad from":           {service: "a", sampleType: "cpu", since: time.Hour, format: "folded", from: "yesterday"},
	} {
		if err := runObsProfile(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, o, run); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestObserveProfileByTraceNeedsNoService(t *testing.T) {
	run, seen := fakeProfileRunner("", cliRows)
	var out bytes.Buffer
	err := runObsProfile(context.Background(), &out, &bytes.Buffer{}, obsProfileOpts{trace: "0af7651916cd43dd8448eb211c80319c", sampleType: "cpu", since: time.Hour, format: "folded"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0], "otel_profiles") {
		t.Errorf("a trace export should make exactly one raw-tier query, saw %v", *seen)
	}
}
