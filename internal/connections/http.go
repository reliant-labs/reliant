// Copyright (c) 2025 Reliant Labs

package connections

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
)

// OAuthHTTP serves the browser half of the broker:
//
//	GET /integrations/oauth/{provider}/start     authenticated
//	GET /integrations/oauth/{provider}/callback  bound by cookie, not by login
//
// The callback is a top-level navigation from the provider, so it carries no
// Authorization header; the flow's session binding (a cookie set at start) is
// what proves it is the same browser. The user comes from the flow row.
type OAuthHTTP struct {
	broker      *Broker
	requireAuth func(http.Handler) http.Handler
	secure      bool
}

// NewOAuthHTTP builds the handlers. requireAuth wraps the start route.
// publicURL decides cookie flavour: an https deployment gets the __Host- prefix
// and Secure; plain http (local self-hosting) cannot set either.
func NewOAuthHTTP(broker *Broker, requireAuth func(http.Handler) http.Handler, publicURL string) *OAuthHTTP {
	return &OAuthHTTP{broker: broker, requireAuth: requireAuth, secure: strings.HasPrefix(publicURL, "https://")}
}

// Register mounts both routes with handle (a ServeMux-compatible registrar).
func (h *OAuthHTTP) Register(handle func(pattern string, handler http.Handler)) {
	handle("GET /integrations/oauth/{provider}/start", h.requireAuth(http.HandlerFunc(h.start)))
	handle("GET /integrations/oauth/{provider}/callback", http.HandlerFunc(h.callback))
}

func (h *OAuthHTTP) cookieName(provider string) string {
	if h.secure {
		return "__Host-reliant_oauth_" + provider
	}
	return "reliant_oauth_" + provider
}

func (h *OAuthHTTP) setBinder(w http.ResponseWriter, provider, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cookieName(provider), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (h *OAuthHTTP) start(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	userID, ok := auth.GetUserIDFromContext(r.Context())
	if !ok || userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	binder := base64.RawURLEncoding.EncodeToString(raw)

	q := r.URL.Query()
	authorizeURL, err := h.broker.Start(r.Context(), StartParams{
		UserID: userID, IntegrationID: provider, Binder: binder, Name: q.Get("name"),
		ReconnectID: q.Get("reconnect_id"), RedirectAfter: q.Get("redirect_after"),
	})
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	h.setBinder(w, provider, binder, int(FlowTTL.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	// A browser cannot attach an Authorization header to a navigation, so the
	// web app fetches this with mode=json (which sets the cookie), then
	// navigates to the returned URL.
	if q.Get("mode") == "json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"authorize_url": authorizeURL})
		return
	}
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

func (h *OAuthHTTP) callback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")

	cookie, _ := r.Cookie(h.cookieName(provider))
	binder := ""
	if cookie != nil {
		binder = cookie.Value
	}
	// The binder is single-use too: clear it whatever happens next.
	h.setBinder(w, provider, "", -1)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")

	if binder == "" {
		h.finish(w, r, "/", "", "session")
		return
	}
	if q.Get("error") != "" {
		landing := h.broker.Abandon(r.Context(), state, binder)
		h.finish(w, r, landing, "", "denied")
		return
	}
	done, err := h.broker.Complete(r.Context(), CompleteParams{ProviderID: provider, State: state, Code: code, Binder: binder})
	if err != nil {
		slog.Warn("oauth callback failed", "integration", provider, "class", httpErrorClass(err))
		h.finish(w, r, "", "", httpErrorClass(err))
		return
	}
	h.finish(w, r, done.RedirectAfter, done.Connection.ID, "")
}

// finish redirects to a validated relative landing path with the outcome as
// query parameters. Neither the code nor the state is ever echoed.
func (h *OAuthHTTP) finish(w http.ResponseWriter, r *http.Request, landing, connectionID, errClass string) {
	path, ok := SafeRedirectAfter(landing)
	if !ok || path == "" {
		path = "/"
	}
	u, err := url.Parse(path)
	if err != nil {
		u = &url.URL{Path: "/"}
	}
	q := u.Query()
	if errClass != "" {
		q.Set("connection_error", errClass)
	} else {
		q.Set("connection", connectionID)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func httpErrorClass(err error) string {
	switch CodeOf(err) {
	case CodeFailedPrecondition:
		return "rejected"
	case CodeUnavailable:
		return "unavailable"
	case CodeInvalidArgument:
		return "invalid"
	case CodeNotFound:
		return "unknown_integration"
	}
	return "internal"
}

func writeHTTPError(w http.ResponseWriter, err error) {
	var e *Error
	status, msg := http.StatusInternalServerError, "internal error"
	if errors.As(err, &e) {
		msg = e.Message
		switch e.Code {
		case CodeNotFound:
			status = http.StatusNotFound
		case CodeInvalidArgument:
			status = http.StatusBadRequest
		case CodeFailedPrecondition, CodeNeedsReauth:
			status = http.StatusConflict
		case CodeUnavailable:
			status = http.StatusServiceUnavailable
		}
	}
	http.Error(w, msg, status)
}
