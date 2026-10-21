package inputoptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

const (
	// callTimeout bounds one action call, which for a walk over every
	// installation of a user is many requests.
	callTimeout = 60 * time.Second
	// maxResolvePages bounds how many upstream pages Resolve reads to look
	// values up in a type that has no lookup action.
	maxResolvePages = 10
	// resolveConcurrency bounds the lookups Resolve runs at once, each of
	// which is a request on the caller's rate limit.
	resolveConcurrency = 4
	maxTokenLen        = 4096
)

type service struct {
	deps   Deps
	cache  *cache
	flight singleflight.Group
	log    *slog.Logger

	projectors sync.Map // type ref -> *projector
	schemas    sync.Map // type ref -> *gojsonschema.Resolved
}

// New builds the service. Types, Runner and Credentials are required.
func New(deps Deps) (Service, error) {
	switch {
	case deps.Types == nil:
		return nil, errors.New("inputoptions: Deps.Types is required")
	case deps.Runner == nil:
		return nil, errors.New("inputoptions: Deps.Runner is required")
	case deps.Credentials == nil:
		return nil, errors.New("inputoptions: Deps.Credentials is required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ttl := deps.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &service{deps: deps, cache: newCache(ttl, deps.Now), log: log}, nil
}

// resolvedType is a declared type with its option source checked once.
type resolvedType struct {
	ref  string
	m    *reliantv1.IntegrationManifest
	t    *reliantv1.TypeSpec
	o    *reliantv1.TypeOptionsSpec
	list *reliantv1.ActionSpec
}

func (s *service) typeFor(ref string) (*resolvedType, error) {
	if ref == "" {
		return nil, &detailError{ErrBadRequest, "type is required"}
	}
	m, t, ok := s.deps.Types.Lookup(ref)
	if !ok {
		return nil, &detailError{ErrUnknownType, fmt.Sprintf("no integration declares the type %q", ref)}
	}
	if t.GetOptions() == nil {
		return nil, &detailError{ErrNoOptions, fmt.Sprintf("%s has no list of values to pick from", ref)}
	}
	list, err := readOnlyAction(m, t.GetOptions().GetAction())
	if err != nil {
		return nil, &detailError{ErrInvalidType, fmt.Sprintf("%s: %v", ref, err)}
	}
	return &resolvedType{ref: ref, m: m, t: t, o: t.GetOptions(), list: list}, nil
}

// readOnlyAction re-checks at call time what the manifest loader checked at
// load: a picker only ever runs an action that reads and that the server may
// place.
func readOnlyAction(m *reliantv1.IntegrationManifest, id string) (*reliantv1.ActionSpec, error) {
	a, ok := manifest.Action(m, id)
	switch {
	case !ok:
		return nil, fmt.Errorf("action %q is not an action of %s", id, m.GetId())
	case a.GetMutates():
		return nil, fmt.Errorf("action %q changes external state", id)
	case a.GetPlacement() == manifest.PlacementDaemon:
		return nil, fmt.Errorf("action %q runs on a daemon", id)
	}
	return a, nil
}

// --- List -------------------------------------------------------------------

func (s *service) List(ctx context.Context, req ListRequest) (*ListResult, error) {
	if req.UserID == "" {
		return nil, &detailError{ErrBadRequest, "no user"}
	}
	if req.PageSize < 0 {
		return nil, &detailError{ErrBadRequest, "page_size must not be negative"}
	}
	rt, err := s.typeFor(req.Type)
	if err != nil {
		return nil, err
	}
	scope, err := s.scopeFor(rt, req.Scope)
	if err != nil {
		return nil, err
	}
	size := req.PageSize
	if size == 0 {
		size = DefaultPageSize
	}
	size = min(size, MaxPageSize)

	query := strings.TrimSpace(req.Query)
	searchParam := searchParamOf(rt.o)
	serverSearch := searchParam != ""
	if serverSearch && query == "" {
		// Nothing to ask the provider yet, so nothing is known about the
		// connection either.
		return &ListResult{State: StateUnknown}, nil
	}
	binding := bindingOf(rt.ref, req.ConnectionID, scope, query)
	tok, err := decodeToken(req.PageToken, binding)
	if err != nil {
		return nil, err
	}

	lst, err := s.listPage(ctx, req.UserID, rt, req.ConnectionID, scope, query, searchParam, tok.Up)
	if errors.Is(err, errNeedsConnection) {
		return &ListResult{State: StateNeedsConnection}, nil
	}
	if err != nil {
		return nil, err
	}
	pr, err := s.projectorFor(rt)
	if err != nil {
		return nil, err
	}
	options, err := pr.options(lst.data, identityOf(req.ActingIdentity), s.log)
	if err != nil {
		return nil, err
	}
	if !serverSearch && query != "" {
		options = filterOptions(options, query)
	}

	res := &ListResult{State: StateConnected}
	if paging := rt.o.GetPaging(); paging != nil {
		// The provider pages: return its page whole and hand on its cursor.
		res.Options = options
		if next := stringAtPath(lst.data, paging.GetNextTokenPath()); next != "" {
			res.NextPageToken = encodeToken(pageToken{K: binding, Up: next})
		}
		return res, nil
	}
	start := min(tok.Off, len(options))
	end := min(start+size, len(options))
	res.Options = options[start:end]
	if end < len(options) {
		res.NextPageToken = encodeToken(pageToken{K: binding, Off: end})
	}
	return res, nil
}

// listPage runs the type's list action for one upstream page.
// searchParamOf is the action param that receives the query, or "" when the
// type is searched client-side.
func searchParamOf(o *reliantv1.TypeOptionsSpec) string {
	name, _ := strings.CutPrefix(o.GetSearch(), manifest.SearchParamPrefix)
	if o.GetSearch() == name {
		return ""
	}
	return name
}

func (s *service) listPage(ctx context.Context, userID string, rt *resolvedType, connectionID string, scope map[string]any, query, searchParam, upstreamToken string) (*listing, error) {
	params, err := renderParams(rt.o.GetParams(), map[string]any{"scope": scope, "query": query, "page_token": upstreamToken})
	if err != nil {
		return nil, &detailError{ErrBadScope, err.Error()}
	}
	if searchParam != "" {
		params[searchParam] = query
	}
	if paging := rt.o.GetPaging(); paging != nil && upstreamToken != "" {
		params[paging.GetTokenParam()] = upstreamToken
	}
	// The query is part of the call only when the provider searches; a
	// client-side search reuses the one cached listing for every keystroke.
	keyQuery := ""
	if searchParam != "" {
		keyQuery = query
	}
	key := cacheKey(userID, rt.ref, connectionID, scope, keyQuery, upstreamToken, "list")
	return s.run(ctx, key, userID, rt, rt.list, params, connectionID)
}

// --- Resolve ----------------------------------------------------------------

func (s *service) Resolve(ctx context.Context, req ResolveRequest) (*Resolution, error) {
	if req.UserID == "" {
		return nil, &detailError{ErrBadRequest, "no user"}
	}
	rt, err := s.typeFor(req.Type)
	if err != nil {
		return nil, err
	}
	values := dedupe(req.Values)
	if len(values) > MaxResolveValues {
		return nil, &detailError{ErrBadRequest, fmt.Sprintf("resolve takes at most %d values", MaxResolveValues)}
	}
	out := &Resolution{State: StateUnknown}
	if len(values) == 0 {
		return out, nil
	}

	// A value the type's own schema refuses was never storable, so it needs
	// no round trip to know it resolves to nothing.
	valid := values[:0:0]
	for _, v := range values {
		if s.validValue(rt.ref, v) {
			valid = append(valid, v)
		} else {
			out.Unreachable = append(out.Unreachable, v)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	scope, err := s.scopeFor(rt, req.Scope)
	if err != nil {
		return nil, err
	}
	identity := identityOf(req.ActingIdentity)
	if rt.o.GetResolve() != nil {
		err = s.resolveByLookup(ctx, req, rt, scope, valid, out)
	} else {
		err = s.resolveByListing(ctx, req, rt, scope, valid, identity, out)
	}
	if errors.Is(err, errNeedsConnection) {
		return &Resolution{State: StateNeedsConnection}, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *service) resolveByLookup(ctx context.Context, req ResolveRequest, rt *resolvedType, scope map[string]any, values []string, out *Resolution) error {
	spec := rt.o.GetResolve()
	action, err := readOnlyAction(rt.m, spec.GetAction())
	if err != nil {
		return &detailError{ErrInvalidType, fmt.Sprintf("%s: resolve: %v", rt.ref, err)}
	}
	pr, err := s.projectorFor(rt)
	if err != nil {
		return err
	}
	type found struct {
		opt     Option
		ok      bool
		missing bool
	}
	results := make([]found, len(values))
	var g errgroup.Group
	g.SetLimit(resolveConcurrency)
	for i, v := range values {
		g.Go(func() error {
			params, err := renderParams(spec.GetParams(), map[string]any{"value": v, "scope": scope})
			if err != nil {
				results[i] = found{missing: true}
				return nil
			}
			key := cacheKey(req.UserID, rt.ref, req.ConnectionID, scope, "", v, "resolve")
			lst, err := s.run(ctx, key, req.UserID, rt, action, params, req.ConnectionID)
			var ue *UpstreamError
			switch {
			case errors.As(err, &ue) && !ue.Retryable && (ue.StatusCode == 404 || ue.StatusCode == 410):
				results[i] = found{missing: true}
				return nil
			case err != nil:
				return err
			}
			opt, ok := pr.resolved(v, lst.data, s.log)
			results[i] = found{opt: opt, ok: ok, missing: !ok}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	out.State = StateConnected
	for i, r := range results {
		switch {
		case r.ok:
			out.Options = append(out.Options, r.opt)
		case r.missing:
			out.Unreachable = append(out.Unreachable, values[i])
		}
	}
	return nil
}

func (s *service) resolveByListing(ctx context.Context, req ResolveRequest, rt *resolvedType, scope map[string]any, values []string, identity string, out *Resolution) error {
	if searchParamOf(rt.o) != "" {
		// A list that can only be searched cannot be read end to end, and
		// the type names no lookup: nothing here can say a value is gone.
		out.Unlisted = append(out.Unlisted, values...)
		return nil
	}
	pr, err := s.projectorFor(rt)
	if err != nil {
		return err
	}
	byValue := map[string]Option{}
	complete := true
	upstream := ""
	for page := 0; ; page++ {
		lst, err := s.listPage(ctx, req.UserID, rt, req.ConnectionID, scope, "", "", upstream)
		if err != nil {
			return err
		}
		options, err := pr.options(lst.data, identity, s.log)
		if err != nil {
			return err
		}
		for _, o := range options {
			if _, dup := byValue[o.Value]; !dup {
				byValue[o.Value] = o
			}
		}
		complete = complete && !lst.truncated
		paging := rt.o.GetPaging()
		upstream = ""
		if paging != nil {
			upstream = stringAtPath(lst.data, paging.GetNextTokenPath())
		}
		if upstream == "" {
			break
		}
		if page+1 >= maxResolvePages {
			complete = false
			break
		}
	}
	out.State = StateConnected
	for _, v := range values {
		switch o, ok := byValue[v]; {
		case ok:
			out.Options = append(out.Options, o)
		case complete && !rt.t.GetFreeText():
			out.Unreachable = append(out.Unreachable, v)
		default:
			out.Unlisted = append(out.Unlisted, v)
		}
	}
	return nil
}

func (s *service) validValue(ref, value string) bool {
	resolved, ok := s.valueSchema(ref)
	if !ok {
		return true
	}
	return resolved.Validate(value) == nil
}

func (s *service) valueSchema(ref string) (*gojsonschema.Resolved, bool) {
	if r, ok := s.schemas.Load(ref); ok {
		resolved := r.(*gojsonschema.Resolved)
		return resolved, resolved != nil
	}
	var resolved *gojsonschema.Resolved
	if _, t, ok := s.deps.Types.Lookup(ref); ok {
		if raw, err := json.Marshal(manifest.ValueSchema(t)); err == nil {
			var sch gojsonschema.Schema
			if json.Unmarshal(raw, &sch) == nil {
				resolved, _ = sch.Resolve(nil)
			}
		}
	}
	s.schemas.Store(ref, resolved)
	return resolved, resolved != nil
}

// --- scope ------------------------------------------------------------------

// scopeFor keeps the scope keys the type declares, drops unset ones, holds
// every set one to its own type's schema, and refuses a missing required one.
func (s *service) scopeFor(rt *resolvedType, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	keys := make([]string, 0, len(rt.o.GetScope()))
	for k := range rt.o.GetScope() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sc := rt.o.GetScope()[key]
		v, set := in[key]
		if !set || isEmpty(v) {
			if sc.GetRequired() {
				return nil, &detailError{ErrBadScope, fmt.Sprintf("%s is required to list %s: choose a %s first", key, rt.ref, sc.GetType())}
			}
			continue
		}
		scopeRef := manifest.QualifyTypeRef(rt.m.GetId(), sc.GetType())
		if resolved, ok := s.valueSchema(scopeRef); ok {
			if err := resolved.Validate(v); err != nil {
				return nil, &detailError{ErrBadScope, fmt.Sprintf("%s is not a valid %s", key, sc.GetType())}
			}
		}
		out[key] = v
	}
	return out, nil
}

func isEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	}
	return false
}

// --- running the action -------------------------------------------------------

var errNeedsConnection = errors.New("inputoptions: no usable connection")

// run performs one call as userID, or returns the cached listing of an
// identical one made moments ago. Concurrent identical calls share one call.
func (s *service) run(ctx context.Context, key, userID string, rt *resolvedType, action *reliantv1.ActionSpec, params map[string]any, connectionID string) (*listing, error) {
	if l, ok := s.cache.get(key); ok {
		return l, nil
	}
	// The call outlives the request that started it so the callers waiting
	// on it do not fail together when the first of them gives up.
	ch := s.flight.DoChan(key, func() (any, error) {
		if l, ok := s.cache.get(key); ok {
			return l, nil
		}
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
		defer cancel()
		return s.call(callCtx, key, userID, rt, action, params, connectionID)
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*listing), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *service) call(ctx context.Context, key, userID string, rt *resolvedType, action *reliantv1.ActionSpec, params map[string]any, connectionID string) (*listing, error) {
	p := make(map[string]any, len(params)+1)
	for k, v := range params {
		p[k] = v
	}
	if connectionID != "" {
		p[httpaction.ConnectionParam] = connectionID
	}
	res, err := s.deps.Runner.RunAuthenticated(ctx, rt.m, action, p, s.deps.Credentials.ForUser(userID), httpaction.CallSite{})
	if err != nil {
		return nil, classify(err, connectionID)
	}
	if res.IsError {
		return nil, &UpstreamError{Message: res.Content, Retryable: res.Retryable, StatusCode: res.StatusCode}
	}
	return s.cache.put(key, res.Data, res.Truncated), nil
}

// classify turns a failure to make the call into the caller's terms.
func classify(err error, connectionID string) error {
	var ce *httpaction.CredentialError
	if !errors.As(err, &ce) {
		return &UpstreamError{Message: err.Error()}
	}
	switch ce.Code {
	case httpaction.CodeNeedsReauth:
		return errNeedsConnection
	case httpaction.CodeFailedPrecondition, httpaction.CodeNotFound:
		if connectionID != "" {
			// Missing and another user's read the same on purpose.
			return &detailError{ErrConnectionNotFound, "no such connection"}
		}
		return errNeedsConnection
	case httpaction.CodeInvalidArgument:
		return &detailError{ErrBadRequest, ce.Message}
	case httpaction.CodeUnavailable:
		return &UpstreamError{Message: ce.Message, Retryable: true}
	}
	return &UpstreamError{Message: ce.Message}
}

// renderParams renders an action's param templates. A template that renders
// to null leaves its param out, so an optional param follows an optional
// scope.
func renderParams(templates map[string]string, vars map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(templates))
	for name, t := range templates {
		v, err := tmpl.RenderWith(t, vars)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if v != nil {
			out[name] = v
		}
	}
	return out, nil
}

// --- page tokens --------------------------------------------------------------

// pageToken is opaque to clients. K binds it to the listing it was issued
// for, so a token cannot be replayed against another type, connection, scope
// or query; Off pages a list the action returned whole, Up is the provider's
// own cursor.
type pageToken struct {
	K   string `json:"k"`
	Off int    `json:"o,omitempty"`
	Up  string `json:"u,omitempty"`
}

func encodeToken(t pageToken) string {
	raw, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeToken(s, binding string) (pageToken, error) {
	if s == "" {
		return pageToken{}, nil
	}
	if len(s) > maxTokenLen {
		return pageToken{}, &detailError{ErrBadPageToken, "page_token is too long"}
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	var t pageToken
	if err != nil || json.Unmarshal(raw, &t) != nil || t.Off < 0 {
		return pageToken{}, &detailError{ErrBadPageToken, "page_token is not one this server issued"}
	}
	if t.K != binding {
		return pageToken{}, &detailError{ErrBadPageToken, "page_token was issued for a different type, connection, scope or query"}
	}
	return t, nil
}

// --- projection ----------------------------------------------------------------

func identityOf(s string) string {
	if s == "app" {
		return "app"
	}
	return "owner"
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func filterOptions(in []Option, query string) []Option {
	q := strings.ToLower(query)
	out := make([]Option, 0, len(in))
	for _, o := range in {
		for _, f := range []string{o.Label, o.Value, o.Description, o.Group} {
			if strings.Contains(strings.ToLower(f), q) {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

// stringAtPath reads a dotted path of object fields, as a string. Anything
// else, or an absent field, is "".
func stringAtPath(data map[string]any, path string) string {
	var cur any = data
	for _, field := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[field]
	}
	str, _ := scalarString(cur)
	return str
}

// scalarString is how a projected value is stored: a string as it is, a whole
// number without a decimal point.
func scalarString(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return v, true
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v)), true
		}
		return fmt.Sprintf("%g", v), true
	case bool:
		return fmt.Sprintf("%t", v), true
	}
	return "", false
}
