// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// HandlerRegistry manages registration of modular handlers
type HandlerRegistry struct {
	router         chi.Router
	authMiddleware *auth.Middleware
	handlers       []ReliantHandler
}

// NewHandlerRegistry creates a new handler registry
func NewHandlerRegistry(router chi.Router, authMiddleware *auth.Middleware) *HandlerRegistry {
	return &HandlerRegistry{
		router:         router,
		authMiddleware: authMiddleware,
		handlers:       make([]ReliantHandler, 0),
	}
}

// Register registers a ReliantHandler and all its routes
func (r *HandlerRegistry) Register(handler ReliantHandler) {
	r.handlers = append(r.handlers, handler)

	prefix := handler.RoutePrefix()
	logging.Debug("Registering handler", "prefix", prefix)

	// Get all routes for this handler
	routes := handler.Routes()

	for _, route := range routes {
		fullPath := prefix + route.Path
		logging.Debug("Registering route", "method", route.Method, "path", fullPath, "auth_required", route.RequireAuth)

		// Wrap the handler with auth middleware if required
		currentRoute := route // Capture route in closure
		var finalHandler http.HandlerFunc
		if currentRoute.RequireAuth {
			if r.authMiddleware == nil {
				panic(fmt.Sprintf("Route requires auth but auth middleware is not configured: %s %s", route.Method, fullPath))
			}
			finalHandler = func(w http.ResponseWriter, req *http.Request) {
				r.authMiddleware.RequireAuth(currentRoute.Handler).ServeHTTP(w, req)
			}
		} else {
			finalHandler = currentRoute.Handler
		}

		// Register the route
		r.router.Method(currentRoute.Method, fullPath, finalHandler)
	}
}

// RegisterAll registers multiple handlers at once
func (r *HandlerRegistry) RegisterAll(handlers ...ReliantHandler) {
	for _, handler := range handlers {
		r.Register(handler)
	}
}

// Cleanup calls cleanup on all registered handlers that support it
func (r *HandlerRegistry) Cleanup() {
	for _, handler := range r.handlers {
		if cleanupHandler, ok := handler.(interface{ Cleanup() }); ok {
			cleanupHandler.Cleanup()
		}
	}
}
