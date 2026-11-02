package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The body Zitadel v4.19.5 returned for GET /v2/settings/password/complexity on
// a default instance (captured against the real server). minLength is a JSON
// STRING: the gateway renders int64 that way.
const defaultComplexityBody = `{"details":{"sequence":"13","changeDate":"2026-10-21T13:41:38.361656Z","resourceOwner":"396100166320390407","creationDate":"2026-10-21T13:41:38.361656Z"},"settings":{"minLength":"8","requiresUppercase":true,"requiresLowercase":true,"requiresNumber":true,"requiresSymbol":true,"resourceOwnerType":"RESOURCE_OWNER_TYPE_INSTANCE"}}`

func TestPasswordPolicy_ReadsTheInstanceSettings(t *testing.T) {
	var gotPath, gotMethod string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte(defaultComplexityBody))
	})
	got, err := client.PasswordPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/settings/password/complexity" || gotMethod != http.MethodGet {
		t.Errorf("called %s %s, want GET /v2/settings/password/complexity", gotMethod, gotPath)
	}
	want := PasswordPolicy{MinLength: 8, HasUppercase: true, HasLowercase: true, HasNumber: true, HasSymbol: true}
	if got != want {
		t.Errorf("policy = %+v, want %+v", got, want)
	}
}

func TestPasswordPolicy_AcceptsANumericMinLengthAndRelaxedPolicies(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"settings":{"minLength":12,"requiresNumber":true}}`))
	})
	got, err := client.PasswordPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := PasswordPolicy{MinLength: 12, HasNumber: true}
	if got != want {
		t.Errorf("policy = %+v, want %+v", got, want)
	}
}

func TestPasswordPolicy_ErrorsArePropagated(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":7,"message":"permission denied"}`))
	})
	if _, err := client.PasswordPolicy(context.Background()); err == nil {
		t.Fatal("a refused policy read was reported as success")
	}
}

// The wire shape is a contract with the scaffolded frontend.
func TestPasswordPolicy_JSONShape(t *testing.T) {
	raw, err := json.Marshal(PasswordPolicy{MinLength: 8, HasUppercase: true, HasSymbol: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"min_length":8,"has_uppercase":true,"has_lowercase":false,"has_number":false,"has_symbol":true}`
	if string(raw) != want {
		t.Errorf("json = %s, want %s", raw, want)
	}
}

func TestPasswordPolicy_Unmet(t *testing.T) {
	full := PasswordPolicy{MinLength: 8, HasUppercase: true, HasLowercase: true, HasNumber: true, HasSymbol: true}
	for _, tc := range []struct {
		name   string
		policy PasswordPolicy
		pw     string
		want   []PasswordRule
	}{
		{"empty breaks everything", full, "", []PasswordRule{RuleMinLength, RuleUppercase, RuleLowercase, RuleNumber, RuleSymbol}},
		// Every rule at once — the server names only the first.
		{"short lowercase only", full, "short", []PasswordRule{RuleMinLength, RuleUppercase, RuleNumber, RuleSymbol}},
		{"too short but otherwise fine", full, "Sh0rt!", []PasswordRule{RuleMinLength}},
		{"no symbol", full, "NoSymbols1234", []PasswordRule{RuleSymbol}},
		{"no number", full, "NoNumbersHere!", []PasswordRule{RuleNumber}},
		{"all met", full, "Password1!", nil},
		// A space is a symbol to the issuer (verified v4.19.5).
		{"space counts as a symbol", full, "Has Space1234", nil},
		// Length is in characters, not bytes.
		{"counts runes", PasswordPolicy{MinLength: 4}, "éééé", nil},
		{"counts runes short", PasswordPolicy{MinLength: 4}, "ééé", []PasswordRule{RuleMinLength}},
		{"a relaxed policy asks for less", PasswordPolicy{MinLength: 6}, "abcdef", nil},
		{"no policy", PasswordPolicy{}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.Unmet(tc.pw); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Unmet(%q) = %v, want %v", tc.pw, got, tc.want)
			}
		})
	}
}

func TestPasswordPolicy_Describe(t *testing.T) {
	p := PasswordPolicy{MinLength: 8}
	for _, tc := range []struct {
		rules []PasswordRule
		want  string
	}{
		{nil, ""},
		{[]PasswordRule{RuleMinLength}, "at least 8 characters"},
		{[]PasswordRule{RuleMinLength, RuleSymbol}, "at least 8 characters and a symbol"},
		{[]PasswordRule{RuleMinLength, RuleUppercase, RuleNumber}, "at least 8 characters, an uppercase letter and a number"},
	} {
		if got := p.Describe(tc.rules); got != tc.want {
			t.Errorf("Describe(%v) = %q, want %q", tc.rules, got, tc.want)
		}
	}
}

// Every id below was observed against a real Zitadel v4.19.4/5. Creating a user
// raises COMMA-…, setting a password raises DOMAIN-…; the tail is what is
// stable, so both prefixes classify.
func TestPolicyErrorFrom_ClassifiesEveryComplexityRule(t *testing.T) {
	for _, tc := range []struct {
		id, msg string
		rule    PasswordRule
	}{
		{"COMMA-HuJf6", "Password is too short", RuleMinLength},
		{"DOMAIN-HuJf6", "Password is too short", RuleMinLength},
		{"COMMA-VoaRj", "Password must contain upper case", RuleUppercase},
		{"COMMA-co3Xw", "Password must contain lower case", RuleLowercase},
		{"COMMA-ZBv4H", "Password must contain number", RuleNumber},
		{"COMMA-ZDLwA", "Password must contain symbol", RuleSymbol},
	} {
		body := `{"code":3,"message":"` + tc.msg + ` (` + tc.id + `)","details":[{"id":"` + tc.id + `","message":"` + tc.msg + `"}]}`
		got := policyErrorFrom(&apiError{status: 400, body: body})
		if got == nil || got.Rule != tc.rule || got.Reason != tc.msg {
			t.Errorf("%s: got %#v, want rule %q reason %q", tc.id, got, tc.rule, tc.msg)
		}
	}
}

func TestPolicyErrorFrom_UnknownDomainRuleIsStillAPolicyRejection(t *testing.T) {
	body := `{"details":[{"id":"DOMAIN-NewRule","message":"Password must not be common"}]}`
	got := policyErrorFrom(&apiError{status: 400, body: body})
	if got == nil || got.Rule != "" || got.Reason != "Password must not be common" {
		t.Errorf("got %#v", got)
	}
}

func TestPolicyErrorFrom_IgnoresOtherRejections(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate user": `{"details":[{"id":"V3-DKcYh","message":"User already exists"}]}`,
		"bad code":       `{"details":[{"id":"CODE-woT0xc","message":"Code is invalid"}]}`,
		"not json":       `upstream connect error`,
		"no details":     `{"code":3,"message":"x"}`,
	} {
		if got := policyErrorFrom(&apiError{status: 400, body: body}); got != nil {
			t.Errorf("%s classified as a policy error: %#v", name, got)
		}
	}
}

// A too-short password at SIGN-UP must come back as something the broker can
// branch on, not the generic sentence the owner hit.
func TestRegister_ReportsAPasswordPolicyRejectionStructurally(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"Password is too short (COMMA-HuJf6)","details":[{"id":"COMMA-HuJf6","message":"Password is too short"}]}`))
	})
	_, err := client.Register(context.Background(), Registration{Email: "a@b.test", Password: "short"})
	var policy *PasswordPolicyError
	if !errors.As(err, &policy) {
		t.Fatalf("err = %v, want a *PasswordPolicyError", err)
	}
	if policy.Rule != RuleMinLength || policy.Reason != "Password is too short" {
		t.Errorf("policy error = %#v", policy)
	}
}

func TestRegister_OtherRejectionsAreNotPolicyErrors(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":6,"message":"User already exists (V3-DKcYh)","details":[{"id":"V3-DKcYh","message":"User already exists"}]}`))
	})
	_, err := client.Register(context.Background(), Registration{Email: "a@b.test", Password: "Password1!"})
	var policy *PasswordPolicyError
	if err == nil || errors.As(err, &policy) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the issuer's reason was lost: %v", err)
	}
}

func TestResetPassword_ClassifiesEveryComplexityRuleAsPolicy(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"details":[{"id":"DOMAIN-ZDLwA","message":"Password must contain symbol"}]}`))
	})
	err := client.ResetPassword(context.Background(), PasswordReset{UserID: "u", Code: "ABC123", NewPassword: "NoSymbols1234"})
	var policy *PasswordPolicyError
	if !errors.As(err, &policy) || policy.Rule != RuleSymbol {
		t.Fatalf("err = %#v, want a symbol policy error", err)
	}
}

func TestSendPasswordResetLink(t *testing.T) {
	var gotPath string
	var got map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"details":{}}`))
	})
	const tmpl = "https://app.example.com/auth/reset?userID={{.UserID}}&code={{.Code}}&orgID={{.OrgID}}"
	if err := client.SendPasswordResetLink(context.Background(), " u1 ", tmpl); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/users/u1/password_reset" {
		t.Errorf("path = %q", gotPath)
	}
	link, _ := got["sendLink"].(map[string]any)
	if link["urlTemplate"] != tmpl || link["notificationType"] != "NOTIFICATION_TYPE_Email" {
		t.Errorf("sendLink = %v", link)
	}
	if _, minted := got["returnCode"]; minted {
		t.Error("a mailed reset must not ask the issuer to hand the code back")
	}
}

func TestSendPasswordResetLink_RequiresAUser(t *testing.T) {
	client := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("issuer contacted") })
	if err := client.SendPasswordResetLink(context.Background(), "  ", ""); err == nil {
		t.Fatal("accepted an empty user id")
	}
}

func TestMintPasswordResetCode(t *testing.T) {
	var got map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"details":{},"verificationCode":"9RJE5L"}`))
	})
	code, err := client.MintPasswordResetCode(context.Background(), "u1")
	if err != nil || code != "9RJE5L" {
		t.Fatalf("code = %q, err = %v", code, err)
	}
	if _, ok := got["returnCode"]; !ok {
		t.Errorf("body = %v, want returnCode", got)
	}

	empty := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"details":{}}`)) })
	if _, err := empty.MintPasswordResetCode(context.Background(), "u1"); err == nil {
		t.Error("a response with no code was reported as success")
	}
}

func TestFindUserByID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		want    User
		wantErr error
	}{
		{"human", 200, `{"user":{"userId":"u1","preferredLoginName":"a@b.test","human":{"email":{"email":"a@b.test"}}}}`, User{ID: "u1", LoginName: "a@b.test", Email: "a@b.test"}, nil},
		{"machine", 200, `{"user":{"userId":"m1","preferredLoginName":"svc"}}`, User{}, ErrUserNotFound},
		{"missing", 404, `{"code":5,"message":"User not found"}`, User{}, ErrUserNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := client.FindUserByID(context.Background(), "u1")
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("got (%+v, %v), want (%+v, %v)", got, err, tc.want, tc.wantErr)
			}
			if gotPath != "/v2/users/u1" {
				t.Errorf("path = %q", gotPath)
			}
		})
	}
}
