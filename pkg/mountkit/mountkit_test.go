package mountkit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/mountkit"
)

// connectOnlyService implements only the required Registrar. It records
// the opts it received so a test can assert the shared chain was threaded
// through unchanged, and mounts a sentinel route so httptest can prove the
// Connect handler actually landed on the mux.
type connectOnlyService struct {
	connectPath  string
	registerCnt  int
	receivedOpts []connect.HandlerOption
}

func (s *connectOnlyService) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	s.registerCnt++
	s.receivedOpts = opts
	mux.HandleFunc(s.connectPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("connect"))
	})
}

// httpService adds the optional HTTPRegistrar capability. It records that
// the stack it was handed is the one the caller supplied (by invoking it),
// and mounts a plain-HTTP route.
type httpService struct {
	connectOnlyService
	httpCnt   int
	stackUsed bool
	httpRoute string
}

func (s *httpService) RegisterHTTP(mux *http.ServeMux, stack func(http.Handler) http.Handler) {
	s.httpCnt++
	final := func(next http.Handler) http.Handler {
		if stack != nil {
			s.stackUsed = true
			return stack(next)
		}
		return next
	}
	mux.Handle(s.httpRoute, final(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))
}

// webhookService adds the optional WebhookRegistrar capability on top of
// Register + RegisterHTTP, exercising the all-capabilities path.
type webhookService struct {
	httpService
	webhookCnt   int
	webhookRoute string
}

func (s *webhookService) RegisterWebhookRoutes(mux *http.ServeMux, _ func(http.Handler) http.Handler) {
	s.webhookCnt++
	mux.HandleFunc(s.webhookRoute, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// notAService implements none of the capabilities.
type notAService struct{}

func TestRegisterService(t *testing.T) {
	// A distinctive opt the fakes can fingerprint; connect.HandlerOption is
	// opaque, so we assert on count + identity rather than its contents.
	sharedOpt := connect.WithReadMaxBytes(1234)

	tests := []struct {
		name string
		// svc is built fresh per case so counters start at zero.
		newSvc func() any
		// routes maps an HTTP path to the status code the mux must return,
		// proving the route was registered.
		routes map[string]int
		// assert runs extra capability-specific checks on the concrete svc.
		assert func(t *testing.T, svc any)
	}{
		{
			name: "connect only",
			newSvc: func() any {
				return &connectOnlyService{connectPath: "/connect.only/"}
			},
			routes: map[string]int{"/connect.only/": http.StatusOK},
			assert: func(t *testing.T, svc any) {
				s := svc.(*connectOnlyService)
				if s.registerCnt != 1 {
					t.Fatalf("Register called %d times, want 1", s.registerCnt)
				}
				if len(s.receivedOpts) != 1 {
					t.Fatalf("got %d opts, want 1 (shared chain not threaded through)", len(s.receivedOpts))
				}
			},
		},
		{
			name: "connect + http",
			newSvc: func() any {
				return &httpService{
					connectOnlyService: connectOnlyService{connectPath: "/svc.http/"},
					httpRoute:          "/webhooks/plain",
				}
			},
			routes: map[string]int{
				"/svc.http/":      http.StatusOK,
				"/webhooks/plain": http.StatusAccepted,
			},
			assert: func(t *testing.T, svc any) {
				s := svc.(*httpService)
				if s.registerCnt != 1 || s.httpCnt != 1 {
					t.Fatalf("Register=%d RegisterHTTP=%d, want 1/1", s.registerCnt, s.httpCnt)
				}
				if !s.stackUsed {
					t.Fatal("caller-supplied HTTP stack was not passed to RegisterHTTP")
				}
			},
		},
		{
			name: "connect + http + webhook",
			newSvc: func() any {
				return &webhookService{
					httpService: httpService{
						connectOnlyService: connectOnlyService{connectPath: "/svc.full/"},
						httpRoute:          "/svc/extra",
					},
					webhookRoute: "/webhooks/declared",
				}
			},
			routes: map[string]int{
				"/svc.full/":         http.StatusOK,
				"/svc/extra":         http.StatusAccepted,
				"/webhooks/declared": http.StatusNoContent,
			},
			assert: func(t *testing.T, svc any) {
				s := svc.(*webhookService)
				if s.registerCnt != 1 || s.httpCnt != 1 || s.webhookCnt != 1 {
					t.Fatalf("Register=%d HTTP=%d Webhook=%d, want 1/1/1",
						s.registerCnt, s.httpCnt, s.webhookCnt)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			svc := tc.newSvc()

			mountkit.RegisterService(
				mux, svc,
				[]connect.HandlerOption{sharedOpt},
				mountkit.WithHTTPStack(func(next http.Handler) http.Handler { return next }),
			)

			srv := httptest.NewServer(mux)
			defer srv.Close()

			for path, wantStatus := range tc.routes {
				resp, err := http.Get(srv.URL + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != wantStatus {
					t.Errorf("GET %s: status %d, want %d", path, resp.StatusCode, wantStatus)
				}
			}

			if tc.assert != nil {
				tc.assert(t, svc)
			}
		})
	}
}

func TestRegisterService_MissingRegistrarPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when svc does not implement Registrar")
		}
	}()
	mountkit.RegisterService(http.NewServeMux(), notAService{}, nil)
}

func TestRegisterService_NilMuxPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil mux")
		}
	}()
	mountkit.RegisterService(nil, &connectOnlyService{connectPath: "/x/"}, nil)
}

// TestRegisterService_NoStackOmitted proves optional routes still mount
// when WithHTTPStack is not supplied (identity stack fallback).
func TestRegisterService_NoStackOmitted(t *testing.T) {
	svc := &httpService{
		connectOnlyService: connectOnlyService{connectPath: "/nostack/"},
		httpRoute:          "/nostack/extra",
	}
	mux := http.NewServeMux()
	mountkit.RegisterService(mux, svc, nil) // no WithHTTPStack

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nostack/extra")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if svc.httpCnt != 1 {
		t.Errorf("RegisterHTTP called %d times, want 1", svc.httpCnt)
	}
}
