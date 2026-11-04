package devidp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// As in login_test.go, the REFUSALS are the tests that matter. The broker's
// credential can set any user's password with no code at all (verified
// against Zitadel v4.19.4), so "a code-less reset never reaches the issuer"
// is the most load-bearing assertion in this file.

func TestResetPassword_RefusesMissingCodeWithoutCallingTheIssuer(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	for _, code := range []string{"", "   "} {
		err := client.ResetPassword(context.Background(), PasswordReset{
			UserID:      "user-1",
			Code:        code,
			NewPassword: "Correct-Horse-1!",
		})
		if err == nil {
			t.Fatalf("ResetPassword accepted code %q — with the broker's credential the issuer "+
				"would overwrite the password with no proof at all", code)
		}
		if !strings.Contains(err.Error(), "no reset code") {
			t.Fatalf("error should name the cause, got: %v", err)
		}
	}
	if called {
		t.Fatal("ResetPassword contacted the issuer despite refusing a code-less reset")
	}
}

func TestResetPassword_RefusesMissingUserOrPassword(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the issuer was contacted for an incomplete reset")
		w.WriteHeader(http.StatusOK)
	})
	for name, r := range map[string]PasswordReset{
		"no user":     {Code: "ABC123", NewPassword: "Correct-Horse-1!"},
		"no password": {UserID: "user-1", Code: "ABC123"},
	} {
		if err := client.ResetPassword(context.Background(), r); err == nil {
			t.Errorf("%s: ResetPassword accepted an incomplete reset", name)
		}
	}
}

func TestResetPassword_SendsCodeAndPassword(t *testing.T) {
	var gotPath string
	var got map[string]any
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"details":{"sequence":"5"}}`))
	})

	err := client.ResetPassword(context.Background(), PasswordReset{
		UserID:      "user-1",
		Code:        " ABC123 ",
		NewPassword: "Correct-Horse-1!",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/v2/users/user-1/password" {
		t.Fatalf("path = %q", gotPath)
	}
	if got["verificationCode"] != "ABC123" {
		t.Fatalf("verificationCode = %#v, want the trimmed code", got["verificationCode"])
	}
	pw, _ := got["newPassword"].(map[string]any)
	if pw["password"] != "Correct-Horse-1!" || pw["changeRequired"] != false {
		t.Fatalf("newPassword = %#v", got["newPassword"])
	}
}

// The two rejections a reset form must tell apart. Bodies are the ones
// Zitadel v4.19.4 actually returned.
func TestResetPassword_ClassifiesIssuerRejections(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantCode   bool
		wantPolicy string
	}{
		{
			name:     "wrong code",
			status:   http.StatusBadRequest,
			body:     `{"code":3,"message":"Code is invalid (CODE-woT0xc)","details":[{"@type":"type.googleapis.com/zitadel.v1.ErrorDetail","id":"CODE-woT0xc","message":"Code is invalid"}]}`,
			wantCode: true,
		},
		{
			name:     "used code",
			status:   http.StatusBadRequest,
			body:     `{"code":9,"message":"Code not found (COMMAND-2M9fs)","details":[{"@type":"type.googleapis.com/zitadel.v1.ErrorDetail","id":"COMMAND-2M9fs","message":"Code not found"}]}`,
			wantCode: true,
		},
		{
			name:       "weak password",
			status:     http.StatusBadRequest,
			body:       `{"code":3,"message":"Password is too short (DOMAIN-HuJf6)","details":[{"@type":"type.googleapis.com/zitadel.v1.ErrorDetail","id":"DOMAIN-HuJf6","message":"Password is too short"}]}`,
			wantPolicy: "Password is too short",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			err := client.ResetPassword(context.Background(), PasswordReset{
				UserID: "user-1", Code: "ABC123", NewPassword: "x",
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrInvalidResetCode); got != tc.wantCode {
				t.Fatalf("errors.Is(ErrInvalidResetCode) = %v, want %v (err: %v)", got, tc.wantCode, err)
			}
			var policy *PasswordPolicyError
			if tc.wantPolicy == "" {
				if errors.As(err, &policy) {
					t.Fatalf("classified as a policy error: %v", err)
				}
				return
			}
			if !errors.As(err, &policy) || policy.Reason != tc.wantPolicy {
				t.Fatalf("want PasswordPolicyError{%q}, got %#v", tc.wantPolicy, err)
			}
		})
	}
}

// A credential failure is NOT a bad code. Reporting it as one would send an
// operator to ask for a new code when the broker's token is what is broken.
func TestResetPassword_CredentialFailureIsNotAnInvalidCode(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":16,"message":"auth header missing"}`))
	})
	err := client.ResetPassword(context.Background(), PasswordReset{
		UserID: "user-1", Code: "ABC123", NewPassword: "x",
	})
	if err == nil || errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("a 401 must surface as a credential error, got: %v", err)
	}
}

func TestFindUserByEmail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    User
		wantErr error
	}{
		{
			// The seeded dev admin: login name and email differ, which is
			// the whole reason this lookup exists.
			name: "one human",
			body: `{"result":[{"userId":"u1","preferredLoginName":"admin@reliant-labs.localhost","human":{"email":{"email":"admin@reliantlabs.io"}}}]}`,
			want: User{ID: "u1", LoginName: "admin@reliant-labs.localhost", Email: "admin@reliantlabs.io"},
		},
		{name: "none", body: `{"result":[]}`, wantErr: ErrUserNotFound},
		{
			name:    "ambiguous",
			body:    `{"result":[{"userId":"u1","preferredLoginName":"a","human":{"email":{"email":"x@y.z"}}},{"userId":"u2","preferredLoginName":"b","human":{"email":{"email":"x@y.z"}}}]}`,
			wantErr: ErrUserNotFound,
		},
		{
			name:    "machine accounts are not candidates",
			body:    `{"result":[{"userId":"m1","preferredLoginName":"broker"}]}`,
			wantErr: ErrUserNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var query map[string]any
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/users" {
					t.Errorf("path = %q", r.URL.Path)
				}
				_ = json.NewDecoder(r.Body).Decode(&query)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := client.FindUserByEmail(context.Background(), " Admin@ReliantLabs.io ")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("user = %#v, want %#v", got, tc.want)
			}
			raw, _ := json.Marshal(query)
			if !strings.Contains(string(raw), `"emailAddress":"Admin@ReliantLabs.io"`) ||
				!strings.Contains(string(raw), "TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE") {
				t.Fatalf("query should be a trimmed, case-insensitive email match: %s", raw)
			}
		})
	}
}

// SignIn must create the session under the account's LOGIN NAME, not the
// email that was typed. Zitadel answers a session for the email of an account
// created with a bare username with "User could not be found" — which locked
// the only prod operator out of sign-in by email.
func TestSignIn_ResolvesEmailToLoginName(t *testing.T) {
	var sessionLoginName string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v2/authorize":
			w.Header().Set("Location", "/auth/sign-in?authRequest=V2_1")
			w.WriteHeader(http.StatusFound)
		case "/v2/users":
			_, _ = w.Write([]byte(`{"result":[{"userId":"u1","preferredLoginName":"admin@reliant-labs.localhost","human":{"email":{"email":"admin@reliantlabs.io"}}}]}`))
		case "/v2/sessions":
			var body struct {
				Checks struct {
					User struct {
						LoginName string `json:"loginName"`
					} `json:"user"`
				} `json:"checks"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sessionLoginName = body.Checks.User.LoginName
			w.WriteHeader(http.StatusBadRequest) // stop the flow here
		default:
			t.Errorf("unexpected call to %q", r.URL.Path)
		}
	})
	_, _ = client.SignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "http://x/cb"},
		Credentials{LoginName: "admin@reliantlabs.io", Password: "pw"})
	if sessionLoginName != "admin@reliant-labs.localhost" {
		t.Fatalf("session created for %q, want the resolved login name", sessionLoginName)
	}
}

// A login name that is not an email is used as typed, with no lookup.
func TestSignIn_PlainLoginNameIsNotLookedUp(t *testing.T) {
	var sessionLoginName string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v2/authorize":
			w.Header().Set("Location", "/auth/sign-in?authRequest=V2_1")
			w.WriteHeader(http.StatusFound)
		case "/v2/sessions":
			var body map[string]map[string]map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sessionLoginName = body["checks"]["user"]["loginName"]
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected call to %q — a bare username must not be searched", r.URL.Path)
		}
	})
	_, _ = client.SignIn(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "http://x/cb"},
		Credentials{LoginName: "admin", Password: "pw"})
	if sessionLoginName != "admin" {
		t.Fatalf("session created for %q, want %q", sessionLoginName, "admin")
	}
}

// BeginFlow must refuse a v1 auth request before any credential is checked.
// Carried on, the flow verifies the password and then fails at finalize with
// "Auth Request does not exist (COMMAND-jae5P)", naming the request rather
// than the LoginV2 setting that caused it. Observed live on a dev instance.
func TestBeginFlow_RefusesV1AuthRequestByName(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v2/authorize" {
			t.Errorf("unexpected call to %q — nothing past /authorize may run for a v1 request", r.URL.Path)
		}
		w.Header().Set("Location", "/ui/login/login?authRequestID=393917801380708354")
		w.WriteHeader(http.StatusFound)
	})
	_, err := client.BeginFlow(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "http://x/cb"})
	if err == nil || !strings.Contains(err.Error(), "LoginV2") {
		t.Fatalf("want an error naming the LoginV2 feature, got: %v", err)
	}
}

func TestBeginFlow_AcceptsV2AuthRequest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://localhost:3002/internal/auth/sign-in?authRequest=V2_393917801380708354")
		w.WriteHeader(http.StatusFound)
	})
	flow, err := client.BeginFlow(context.Background(), FlowConfig{ClientID: "c", RedirectURI: "http://x/cb"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flow.AuthRequestID != "V2_393917801380708354" {
		t.Fatalf("AuthRequestID = %q", flow.AuthRequestID)
	}
}
