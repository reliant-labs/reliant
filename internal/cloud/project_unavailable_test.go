package cloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two answers a control plane gives for a hosted project id the caller
// cannot see: today's (NotFound with ReasonHostedProjectNotFound, the same as
// for an id that never existed), and an older one's (PermissionDenied with a
// fixed sentence, for a project of another org).
var projectAnswers = map[string]struct {
	status int
	reason string
	body   string
}{
	"current: not_found": {404, ReasonHostedProjectNotFound, `{"code":"not_found","message":"hosted project not found"}`},
	"legacy: a person":   {403, "", `{"code":"permission_denied","message":"project not available to this caller"}`},
	"legacy: a machine":  {403, "", `{"code":"permission_denied","message":"capability does not authorize this project"}`},
}

func answer(w http.ResponseWriter, status int, reason, body string) {
	w.Header().Set("Content-Type", "application/json")
	if reason != "" {
		w.Header().Set(ReasonHeader, reason)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// A request carrying Forge-Project-ID gets one error for both answers: a
// *ProjectUnavailableError that names the project and the fix, and that no
// caller can mistake for a NotFound about what it asked for (it does not
// unwrap to *Error, so "an older control plane without GetCapabilities" and
// "no such environment" branches never fire on it).
func TestAProjectTheCallerCannotSeeIsOneErrorWhicheverAnswer(t *testing.T) {
	// The one message every answer must produce, endpoint aside.
	messages := map[string]string{}
	for name, a := range projectAnswers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Forge-Project-ID") != "hp-gone" {
					t.Errorf("Forge-Project-ID = %q", r.Header.Get("Forge-Project-ID"))
				}
				answer(w, a.status, a.reason, a.body)
			}))
			defer srv.Close()
			for _, kind := range []string{"human", "delegated"} {
				client := NewClient(Endpoint{URL: srv.URL, ProjectID: "hp-gone"}, Credential{Kind: kind, Token: "tok"})
				err := client.Call(context.Background(), "controlplane.v1.DeployService/GetCapabilities", map[string]any{}, nil)
				var pu *ProjectUnavailableError
				if !errors.As(err, &pu) || !errors.Is(err, ErrHostedProjectUnavailable) || pu.ProjectID != "hp-gone" {
					t.Fatalf("%s: err = %#v, want a ProjectUnavailableError for hp-gone", kind, err)
				}
				var wire *Error
				if errors.As(err, &wire) {
					t.Fatalf("%s: the error unwraps to the wire *Error (code %q): a NotFound branch would read it as its own answer", kind, wire.Code)
				}
				for _, want := range []string{"hosted project hp-gone is not available", "no project with that id exists there", "an organization this credential does not act for", ".forge/hosted-projects.json"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%s: message lacks %q:\n%s", kind, want, err)
					}
				}
				if strings.Contains(err.Error(), "forge login") {
					t.Errorf("%s: message sends the person to `forge login`, which cannot help:\n%s", kind, err)
				}
				if strings.Contains(err.Error(), pu.Wire.Message) {
					t.Errorf("%s: message carries the server's own text %q, so the answers read differently:\n%s", kind, pu.Wire.Message, err)
				}
				messages[name+"/"+kind] = strings.ReplaceAll(err.Error(), srv.URL, "<endpoint>")
			}
		})
	}
	var first string
	for key, msg := range messages {
		if first == "" {
			first = msg
			continue
		}
		if msg != first {
			t.Errorf("%s reads differently from the other answers:\n%s\nvs\n%s", key, msg, first)
		}
	}
	if len(messages) != 2*len(projectAnswers) {
		t.Fatalf("compared %d messages, want %d", len(messages), 2*len(projectAnswers))
	}
}

// Everything else keeps its own answer: a NotFound about the resource a
// request addressed (no project reason), a PermissionDenied about anything but
// the project, and any answer to a request that named no project id.
func TestOtherAnswersAreNotAProjectTheCallerCannotSee(t *testing.T) {
	for name, tc := range map[string]struct {
		projectID, status, reason, body string
	}{
		"a resource NotFound":         {"hp-1", "404", "", `{"code":"not_found","message":"target not found"}`},
		"a missing scope":             {"hp-1", "403", "", `{"code":"permission_denied","message":"missing deploy:read in target organization"}`},
		"no project id, legacy words": {"", "403", "", `{"code":"permission_denied","message":"project not available to this caller"}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				status := http.StatusNotFound
				if tc.status == "403" {
					status = http.StatusForbidden
				}
				answer(w, status, tc.reason, tc.body)
			}))
			defer srv.Close()
			client := NewClient(Endpoint{URL: srv.URL, ProjectID: tc.projectID}, Credential{Kind: "delegated", Token: "tok"})
			err := client.Call(context.Background(), "controlplane.v1.DeployService/GetEnvironment", map[string]any{"environmentId": "e"}, nil)
			var wire *Error
			if errors.Is(err, ErrHostedProjectUnavailable) || !errors.As(err, &wire) {
				t.Fatalf("err = %#v, want the wire error untouched", err)
			}
		})
	}
}

// ResolveOrganization asks ResolveHostedProject for the bound id itself, so a
// plain NotFound — what every control plane answers for an id that never
// existed — is about the project too, and reads the same as the other two.
func TestResolveOrganizationOfABoundProjectTheCallerCannotSee(t *testing.T) {
	answers := map[string]struct {
		status int
		reason string
		body   string
	}{"older not_found, no reason": {404, "", `{"code":"not_found","message":"project not found not found"}`}}
	for name, a := range projectAnswers {
		answers[name] = a
	}
	for name, a := range answers {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/ResolveHostedProject") {
					t.Errorf("called %s", r.URL.Path)
				}
				answer(w, a.status, a.reason, a.body)
			}))
			defer srv.Close()
			client := NewClient(Endpoint{URL: srv.URL, ProjectID: "hp-gone", ProjectName: "shop"}, Credential{Kind: "human", Token: "tok"})
			_, err := ResolveOrganization(context.Background(), client)
			if !errors.Is(err, ErrHostedProjectUnavailable) {
				t.Fatalf("err = %v, want ErrHostedProjectUnavailable", err)
			}
		})
	}
}
