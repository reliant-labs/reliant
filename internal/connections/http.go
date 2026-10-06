// Copyright (c) 2025 Reliant Labs

package connections

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
)

// OAuthHTTP serves the one browser-facing route of the broker:
//
//	GET /integrations/oauth/{provider}/callback
//
// It is the redirect URI registered with each provider, and it finishes
// nothing: the browser that arrives here carries no credential for this
// server (see Broker). It relays the code to the client that started the flow,
// which finishes it with the CompleteOAuth RPC. A flow starts with the
// StartOAuth RPC, which returns the provider URL directly, so there is no
// start route.
type OAuthHTTP struct {
	broker *Broker
}

// NewOAuthHTTP builds the handler.
func NewOAuthHTTP(broker *Broker) *OAuthHTTP {
	return &OAuthHTTP{broker: broker}
}

// Register mounts the route with handle (a ServeMux-compatible registrar).
func (h *OAuthHTTP) Register(handle func(pattern string, handler http.Handler)) {
	handle("GET /integrations/oauth/{provider}/callback", http.HandlerFunc(h.callback))
}

func (h *OAuthHTTP) callback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	q := r.URL.Query()
	w.Header().Set("Cache-Control", "no-store")
	// The relay URL carries the code; it must not leak to the next page.
	w.Header().Set("Referrer-Policy", "no-referrer")

	target, err := h.broker.Relay(r.Context(), RelayParams{
		ProviderID: provider, State: q.Get("state"), Code: q.Get("code"), Error: q.Get("error"),
	})
	if err != nil {
		// With no flow there is nowhere to send the browser, so the page says
		// what happened instead. Nothing from the request is echoed.
		slog.Warn("oauth callback not relayed", "integration", provider, "class", httpErrorClass(err))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(httpStatus(err))
		_ = callbackErrorPage.Execute(w, errorMessage(err))
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

var callbackErrorPage = template.Must(template.New("callback").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connection not completed</title>
<style>body{font:15px/1.5 -apple-system,system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;padding:0 1rem}main{max-width:28rem;text-align:center}</style>
<main><h1>Connection not completed</h1><p>{{.}}</p><p>Return to Reliant and connect again.</p></main>
`))

func errorMessage(err error) string {
	var e *Error
	if errors.As(err, &e) && e.Code != CodeInternal {
		return e.Message
	}
	return "Something went wrong."
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

func httpStatus(err error) int {
	switch CodeOf(err) {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeInvalidArgument, CodeFailedPrecondition:
		return http.StatusBadRequest
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
