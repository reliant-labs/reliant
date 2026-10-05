// Copyright (c) 2025 Reliant Labs
package modelendpoints

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
)

// ValidationError is a problem with what the user submitted. Its message is
// shown to them verbatim.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

const (
	maxNameLen      = 80
	maxModels       = 256
	maxHeaders      = 16
	maxHeaderValue  = 4096
	maxEndpointsPer = 32
)

// NormalizeBaseURL validates and canonicalizes an endpoint base URL: http or
// https, a host, no userinfo (credentials have one home, and it is not the
// URL), no query or fragment, no trailing slash.
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", invalid("Enter the server's base URL, e.g. https://llm.example.com/v1.")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", invalid("That doesn't look like a URL. Try https://llm.example.com/v1.")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid("The URL must start with http:// or https://.")
	}
	if u.Hostname() == "" {
		return "", invalid("The URL has no host.")
	}
	if u.User != nil {
		return "", invalid("The URL must not contain a username or password. Put an API key in the API key field instead.")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", invalid("The URL must not contain a query string or fragment.")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", invalid("The URL's port is not valid.")
		}
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// normalizedInput is a validated ModelEndpointInput.
type normalizedInput struct {
	Name     string
	BaseURL  string
	Route    string
	DaemonID string
	Models   []*reliantv1.ModelEndpointModel
}

func routeToDB(r reliantv1.ModelEndpointRoute) (string, error) {
	switch r {
	case reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT:
		return db.ModelEndpointRouteDirect, nil
	case reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON:
		return db.ModelEndpointRouteViaDaemon, nil
	}
	return "", invalid("Choose how Reliant should reach this server: from Reliant's cloud, or through one of your machines.")
}

func routeFromDB(r string) reliantv1.ModelEndpointRoute {
	switch r {
	case db.ModelEndpointRouteDirect:
		return reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_DIRECT
	case db.ModelEndpointRouteViaDaemon:
		return reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_VIA_DAEMON
	}
	return reliantv1.ModelEndpointRoute_MODEL_ENDPOINT_ROUTE_UNSPECIFIED
}

// validateInput checks everything that needs no I/O: shape, route/daemon
// pairing, headers and per-model settings. Route policy (does the user own the
// daemon; may the cloud reach this host) is the caller's, since it needs I/O.
func validateInput(in *reliantv1.ModelEndpointInput) (*normalizedInput, error) {
	if in == nil {
		return nil, invalid("Endpoint details are required.")
	}
	name := strings.TrimSpace(in.GetName())
	if name == "" {
		return nil, invalid("Give the endpoint a name.")
	}
	if len([]rune(name)) > maxNameLen {
		return nil, invalid("The name is too long (max %d characters).", maxNameLen)
	}
	baseURL, err := NormalizeBaseURL(in.GetBaseUrl())
	if err != nil {
		return nil, err
	}
	route, err := routeToDB(in.GetRoute())
	if err != nil {
		return nil, err
	}
	daemonID := strings.TrimSpace(in.GetDaemonId())
	switch route {
	case db.ModelEndpointRouteViaDaemon:
		if daemonID == "" {
			return nil, invalid("Choose which of your machines should reach this server.")
		}
	case db.ModelEndpointRouteDirect:
		if daemonID != "" {
			return nil, invalid("A machine can only be chosen when the endpoint is reached through a machine.")
		}
	}
	if len(in.GetHeaders()) > maxHeaders {
		return nil, invalid("Too many headers (max %d).", maxHeaders)
	}
	for k, v := range in.GetHeaders() {
		if err := validateHeaderName(k); err != nil {
			return nil, err
		}
		if len(v) > maxHeaderValue {
			return nil, invalid("The value of header %q is too long.", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, invalid("The value of header %q must be a single line.", k)
		}
	}
	models, err := validateModels(in.GetModels())
	if err != nil {
		return nil, err
	}
	return &normalizedInput{Name: name, BaseURL: baseURL, Route: route, DaemonID: daemonID, Models: models}, nil
}

// reservedHeaders are owned by the transport; letting a user set them would
// break framing or smuggle a second credential past the key field.
var reservedHeaders = map[string]bool{
	"authorization": true, "host": true, "content-length": true, "content-type": true,
	"connection": true, "transfer-encoding": true, "accept-encoding": true, "te": true, "upgrade": true,
}

func validateHeaderName(name string) error {
	if name == "" || len(name) > 128 {
		return invalid("A header name is empty or too long.")
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return invalid("Header name %q may only contain letters, digits, '-' and '_'.", name)
		}
	}
	if reservedHeaders[strings.ToLower(name)] {
		return invalid("Header %q is set by Reliant. Use the API key field for Authorization.", name)
	}
	return nil
}

func validateModels(in []*reliantv1.ModelEndpointModel) ([]*reliantv1.ModelEndpointModel, error) {
	if len(in) > maxModels {
		return nil, invalid("Too many models configured (max %d).", maxModels)
	}
	seen := map[string]bool{}
	out := make([]*reliantv1.ModelEndpointModel, 0, len(in))
	for _, m := range in {
		name := strings.TrimSpace(m.GetName())
		if name == "" {
			return nil, invalid("A model has no name.")
		}
		if seen[name] {
			return nil, invalid("Model %q is listed twice.", name)
		}
		seen[name] = true
		if m.GetContextWindow() < 0 || m.GetMaxOutputTokens() < 0 {
			return nil, invalid("Model %q: context window and max output must not be negative.", name)
		}
		if m.GetContextWindow() > 0 && m.GetMaxOutputTokens() > m.GetContextWindow() {
			return nil, invalid("Model %q: max output can't exceed the context window.", name)
		}
		if m.Temperature != nil && (m.GetTemperature() < 0 || m.GetTemperature() > 2) {
			return nil, invalid("Model %q: temperature must be between 0 and 2.", name)
		}
		if m.TopP != nil && (m.GetTopP() <= 0 || m.GetTopP() > 1) {
			return nil, invalid("Model %q: top_p must be above 0 and at most 1.", name)
		}
		if _, err := local.ValidateExtraBody(m.GetExtraBodyJson()); err != nil {
			return nil, invalid("Model %q: %s.", name, strings.TrimSuffix(err.Error(), "."))
		}
		clean := &reliantv1.ModelEndpointModel{
			Name: name, Hidden: m.GetHidden(), ContextWindow: m.GetContextWindow(), MaxOutputTokens: m.GetMaxOutputTokens(),
			SupportsTools: m.SupportsTools, SupportsVision: m.SupportsVision, SupportsThinking: m.SupportsThinking,
			Temperature: m.Temperature, TopP: m.TopP, ExtraBodyJson: strings.TrimSpace(m.GetExtraBodyJson()),
		}
		out = append(out, clean)
	}
	return out, nil
}

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}
