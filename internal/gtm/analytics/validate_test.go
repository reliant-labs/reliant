// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/gtm"
)

func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"page_viewed", true},
		{"chat.started", true},
		{"gtm:item-sent", true},
		{"A9", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"", false},
		{"it's", false},
		{`back\slash`, false},
		{"two words", false},
		{"semi;colon", false},
		{"paren(", false},
		{"brace{x}", false},
		{"new\nline", false},
		{"tab\t", false},
		{"ünïcode", false},
		{"--comment", true}, // only ever a literal: '--comment'
		{"a/*b*/", false},
		{"' OR 1=1 --", false},
	} {
		if got := validName(tc.name); got != tc.ok {
			t.Errorf("validName(%q) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// FuzzName: whatever validName accepts is plain: no quote, backslash, space
// or control character can reach a literal, and lit of it is the name
// quoted, untouched.
func FuzzName(f *testing.F) {
	for _, s := range []string{"page_viewed", "it's", `\`, "a b", "{from:DateTime64(9)}", "x'; DROP TABLE y --", "é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !validName(s) {
			return
		}
		if s == "" || len(s) > 64 {
			t.Fatalf("accepted %q", s)
		}
		for _, r := range s {
			ok := r == '_' || r == '.' || r == ':' || r == '-' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				t.Fatalf("accepted %q with %q", s, r)
			}
		}
		if got := lit(s); got != "'"+s+"'" {
			t.Fatalf("lit(%q) = %s", s, got)
		}
		q := gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{s}, Property: "route", From: from, To: to, Limit: 5}
		if err := validate(tenantZero, q); err == nil {
			plain := q
			plain.Events = []string{"x"}
			if sql := buildSQL(q); strings.Count(sql, "'") != strings.Count(buildSQL(plain), "'") ||
				strings.Replace(sql, "EventName = "+lit(s), "EventName = "+lit("x"), 1) != buildSQL(plain) {
				t.Fatalf("a name changed the statement beyond its literal:\n%s", sql)
			}
		}
	})
}

func TestIdentifierProperty(t *testing.T) {
	for _, p := range []string{
		"userId", "user_id", "UserID", "user", "session.id", "sessionId", "email", "emailAddress",
		"ip", "client_ip", "uuid", "anonId", "orgId", "org", "api_key", "token", "visitor", "deviceId", "id", "account_id", "HTTPUserID",
		// All-lowercase compounds and the words a first cut missed (review of #3843).
		"customerid", "accountid", "visitorid", "anonymousid", "clientid", "orgid", "traceid", "emailaddress", "useremail",
		"ipaddress", "phonenumber", "apikey", "accesskey", "x-forwarded-for", "name", "full_name", "first_name", "lastName",
		"login", "handle", "address", "ssn", "dob", "card", "iban", "fingerprint", "cookie", "jwt", "gclid", "fbclid",
		"msclkid", "idfa", "adid", "imei",
		// More of the same shapes.
		"plan_id", "planids", "user-agent", "userAgent", "MAC", "mac_address", "sessionuuid", "access_token", "refresh-token",
		"password", "credit_card", "cardNumber", "birthDate", "date_of_birth", "screenName", "username", "ttclid", "wbraid",
		// Second review of #3843.
		"ipv4", "ipv6", "client_ipv4", "mobile", "mobile_number", "cell", "cell_phone", "contact", "contact_email", "recipient",
		"sender", "credential", "otp", "otp_code", "pin", "signature", "serial", "serial_number", "accountnumber",
		"account_number", "referral_code", "invite_code", "inviteCode", "location", "lat", "lng", "geo.lat", "latitude",
		"geolocation",
		// Near the allowed category names, still refused.
		"user_name", "plan_owner_name", "session_token", "auth_token", "card_number", "user_email", "org_id", "iphone_serial",
		"mobileNumber", "Mobile", "mobile_no", "mobileNo", "mobile_num", "contact_form_email", "sender_email", "location_id", "user_names",
	} {
		if !identifierProperty(p) {
			t.Errorf("identifierProperty(%q) = false, want true (words %v)", p, words(p))
		}
	}
	for _, p := range []string{
		"route", "plan", "paid", "android", "valid", "browser", "country", "referrer", "utm_source", "status", "variant",
		"HTTPStatus", "flag", "is_valid", "prepaid", "android_version", "device_type", "client", "account_type", "theme",
		"locale", "os", "region", "utm_campaign", "tier", "billing.interval", "hybrid_mode", "grid",
		// The explicit category names (camelCase is the same name).
		"plan_name", "os_name", "browser_name", "feature_name", "experiment_name", "flag_key", "feature_key",
		"input_tokens", "output_tokens", "token_count", "auth_method", "auth_provider", "cookie_consent", "user_role",
		"user_type", "session_count", "card_type", "org_plan", "iphone", "planName", "inputTokens", "UserRole",
		"pinned", "cellular_generation",
		// Third review of #3843: mobile only as a whole name; more category names.
		"is_mobile", "mobile_os", "mobile_app", "isMobile", "contact_form_variant", "sender_domain", "location_type",
		"iPhone", "IPHONE", "plan_names", "planNames", "feature_names",
	} {
		if identifierProperty(p) {
			t.Errorf("identifierProperty(%q) = true, want false (words %v)", p, words(p))
		}
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string][]string{
		"userId":      {"user", "id"},
		"user_id":     {"user", "id"},
		"HTTPStatus":  {"http", "status"},
		"HTTPUserID":  {"http", "user", "id"},
		"a.b:c-d":     {"a", "b", "c", "d"},
		"utm_source":  {"utm", "source"},
		"route":       {"route"},
		"page2View":   {"page2", "view"},
		"__x__":       {"x"},
		"sessionUUID": {"session", "uuid"},
	} {
		if got := words(in); !slices.Equal(got, want) {
			t.Errorf("words(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	day := 24 * time.Hour
	ok := gtm.AnalyticsQuery{Template: gtm.AnalyticsEventCounts, From: from, To: to, Limit: 50}
	with := func(f func(*gtm.AnalyticsQuery)) gtm.AnalyticsQuery {
		q := ok
		f(&q)
		return q
	}
	for _, tc := range []struct {
		name  string
		org   string
		q     gtm.AnalyticsQuery
		want  error // nil: valid
		isArg bool  // a gtm.ValidationError (gtm.ErrInvalidArgument)
	}{
		{name: "ok", org: tenantZero, q: ok},
		{name: "no org", org: "", q: ok, want: errNoOrg},
		{name: "blank org", org: "  ", q: ok, want: errNoOrg},
		{name: "unknown template", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Template = "raw_sql" }), isArg: true},
		{name: "bad event", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Events = []string{"x'y"} }), isArg: true},
		{name: "zero window", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.From, q.To = time.Time{}, time.Time{} }), isArg: true},
		{name: "unresolved limit", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Limit = 0 }), isArg: true},
		{name: "limit over cap", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Limit = gtm.MaxAnalyticsRows + 1 }), isArg: true},
		{name: "limit at cap", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Limit = gtm.MaxAnalyticsRows })},
		{name: "sql 30 days", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.From = q.To.Add(-30 * day) })},
		{name: "sql 31 days", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.From = q.To.Add(-31 * day) }), want: errWindowTooLong},
		{name: "trend 31 days", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsEventTrend, Events: []string{"a"}, From: to.Add(-31 * day), To: to, Limit: 10}, want: errWindowTooLong},
		{name: "breakdown 31 days", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"a"}, Property: "route", From: to.Add(-31 * day), To: to, Limit: 10}, want: errWindowTooLong},
		{name: "funnel 90 days", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: to.Add(-90 * day), To: to, Limit: 10}},
		{name: "funnel 91 days", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b"}, From: to.Add(-91 * day), To: to, Limit: 10}, isArg: true},
		{name: "funnel duplicate step", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a", "b", "a"}, From: from, To: to, Limit: 10}, want: errDuplicateStep},
		{name: "funnel one step", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsFunnel, Events: []string{"a"}, From: from, To: to, Limit: 10}, isArg: true},
		{name: "trend 11 events", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsEventTrend, Events: strings.Split("a b c d e f g h i j k", " "), From: from, To: to, Limit: 10}, isArg: true},
		{name: "breakdown identifier property", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"a"}, Property: "userId", From: from, To: to, Limit: 10}, want: errIdentifierProperty},
		{name: "breakdown bad property", org: tenantZero, q: gtm.AnalyticsQuery{Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"a"}, Property: "Props['x']", From: from, To: to, Limit: 10}, isArg: true},
		{name: "property on counts", org: tenantZero, q: with(func(q *gtm.AnalyticsQuery) { q.Property = "route" }), isArg: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(tc.org, tc.q)
			switch {
			case tc.isArg:
				if !errors.Is(err, gtm.ErrInvalidArgument) {
					t.Fatalf("err = %v, want an invalid argument", err)
				}
			case tc.want != nil:
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
			case err != nil:
				t.Fatalf("err = %v, want valid", err)
			}
		})
	}
}
