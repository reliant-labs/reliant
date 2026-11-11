package release

import (
	"errors"
	"strings"
	"testing"
)

func uptimeCheck(name string) ShapeUptimeCheck {
	return ShapeUptimeCheck{
		Name: name, URL: "https://shop.example.com/healthz", Method: "GET",
		IntervalSeconds: 60, TimeoutSeconds: 10, Source: UptimeSourceUser,
	}
}

func TestUptimeCheckValidation(t *testing.T) {
	ok := uptimeCheck("shop")
	if err := ok.validate(); err != nil {
		t.Fatalf("a plain check is refused: %v", err)
	}
	target := ShapeUptimeCheck{Name: "site", Target: "site", Path: "/health", Method: "HEAD", IntervalSeconds: 30, TimeoutSeconds: 5, Source: UptimeSourceDefault}
	if err := target.validate(); err != nil {
		t.Fatalf("a target check is refused: %v", err)
	}

	for name, tc := range map[string]struct {
		mutate func(*ShapeUptimeCheck)
		want   string
	}{
		"bad name":        {func(c *ShapeUptimeCheck) { c.Name = "Shop!" }, "name"},
		"no target":       {func(c *ShapeUptimeCheck) { c.URL = "" }, "no target"},
		"two targets":     {func(c *ShapeUptimeCheck) { c.Target = "web" }, "one target"},
		"path with url":   {func(c *ShapeUptimeCheck) { c.Path = "/x" }, "path"},
		"relative path":   {func(c *ShapeUptimeCheck) { c.URL, c.Target, c.Path = "", "web", "x" }, "must start with '/'"},
		"bad scheme":      {func(c *ShapeUptimeCheck) { c.URL = "ftp://shop.example.com/" }, "http or https"},
		"credential":      {func(c *ShapeUptimeCheck) { c.URL = "https://u:p@shop.example.com/" }, "credential"},
		"bad method":      {func(c *ShapeUptimeCheck) { c.Method = "POST" }, "method"},
		"interval floor":  {func(c *ShapeUptimeCheck) { c.IntervalSeconds, c.TimeoutSeconds = 10, 5 }, "interval"},
		"interval ceil":   {func(c *ShapeUptimeCheck) { c.IntervalSeconds = 3600 }, "interval"},
		"timeout >= ival": {func(c *ShapeUptimeCheck) { c.TimeoutSeconds = 60 }, "timeout"},
		"bad status":      {func(c *ShapeUptimeCheck) { c.ExpectStatus = []string{"20x"} }, "expect_status"},
		"body on HEAD":    {func(c *ShapeUptimeCheck) { c.Method, c.ExpectBody = "HEAD", "ok" }, "expect_body"},
		"body too long":   {func(c *ShapeUptimeCheck) { c.ExpectBody = strings.Repeat("x", MaxUptimeExpectBody+1) }, "expect_body"},
		"unknown source":  {func(c *ShapeUptimeCheck) { c.Source = "guess" }, "source"},
	} {
		c := uptimeCheck("shop")
		tc.mutate(&c)
		err := c.validate()
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalid mentioning %q", name, err, tc.want)
		}
	}
}

// A check's url is declared by the author, so it is the SSRF surface: a private
// destination is refused when it is declared, by the same predicate the
// prober's dialer enforces at dial time.
func TestUptimeCheckRefusesAPrivateDestinationAtDeclaration(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/healthz",
		"http://localhost:8080/",
		"https://10.0.0.5/",
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://[::1]/",
		"http://[::ffff:10.0.0.1]/",
		"http://api.default.svc.cluster.local/healthz",
		"http://2130706433/",
		"http://printer/",
	} {
		c := uptimeCheck("x")
		c.URL = raw
		err := c.validate()
		if err == nil || !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepted (err=%v)", raw, err)
		}
	}
	c := uptimeCheck("x")
	c.URL = "https://example.com:8443/status?probe=1"
	if err := c.validate(); err != nil {
		t.Errorf("a public https url with a port and a query is refused: %v", err)
	}
}

func TestUptimeCheckStatusOK(t *testing.T) {
	c := uptimeCheck("x")
	for status, want := range map[int]bool{200: true, 204: true, 301: true, 302: true, 404: false, 500: false, 503: false} {
		if got := c.StatusOK(status); got != want {
			t.Errorf("default: %d = %v, want %v", status, got, want)
		}
	}
	c.ExpectStatus = []string{"200", "4xx"}
	for status, want := range map[int]bool{200: true, 201: false, 301: false, 404: true, 499: true, 500: false} {
		if got := c.StatusOK(status); got != want {
			t.Errorf("200+4xx: %d = %v, want %v", status, got, want)
		}
	}
}

func TestShapeCanonicalSortsAndNormalizesUptimeChecks(t *testing.T) {
	b, a := uptimeCheck("b"), uptimeCheck("a")
	a.ExpectStatus = []string{"3xx", "2xx"} // the default, spelled out and out of order
	b.Path = ""
	explicit := Shape{Kind: EnvLocal, UptimeChecks: []ShapeUptimeCheck{b, a}}
	a2 := uptimeCheck("a") // the same default, left out
	implicit := Shape{Kind: EnvLocal, UptimeChecks: []ShapeUptimeCheck{a2, b}}

	got := explicit.Canonical().UptimeChecks
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("checks not sorted by name: %+v", got)
	}
	if got[0].ExpectStatus != nil {
		t.Errorf("the default statuses are recorded: %v", got[0].ExpectStatus)
	}
	e1, err1 := explicit.Encode()
	e2, err2 := implicit.Encode()
	if err1 != nil || err2 != nil || string(e1) != string(e2) {
		t.Errorf("a producer that spells the defaults out encodes differently: %v %v\n%s\n%s", err1, err2, e1, e2)
	}
	// Shapes recorded before the field are unchanged.
	if got := (Shape{Kind: EnvLocal}).Canonical().UptimeChecks; got != nil {
		t.Errorf("an env with no checks records %v", got)
	}
}

func TestShapeValidateRefusesDuplicateAndInvalidUptimeChecks(t *testing.T) {
	s := Shape{Kind: EnvLocal, UptimeChecks: []ShapeUptimeCheck{uptimeCheck("a"), uptimeCheck("a")}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate names: %v", err)
	}
	bad := uptimeCheck("b")
	bad.URL = "http://10.1.2.3/"
	s = Shape{Kind: EnvLocal, UptimeChecks: []ShapeUptimeCheck{bad}}
	if err := s.Validate(); err == nil {
		t.Error("a private url survives Shape.Validate: a shape is stored forever and read by another program")
	}
}
