package observe

import "context"

// componentMethodIDKey is the context key of a component call's frame
// (componentFrame / argsFrame in component_op.go): its catalog method ID and,
// when the call carries one, its Op and arguments.
type componentMethodIDKey struct{}

// WithMethodIDs returns an independent chain with generated, bounded method
// metadata. Legacy method names, tracer scopes and metric dimensions are kept.
// A nil chain remains a pass-through. No request data enters these identities.
func (c *ComponentChain) WithMethodIDs(ids map[string]string) *ComponentChain {
	if c == nil {
		return nil
	}
	copyIDs := make(map[string]string, len(ids))
	for k, v := range ids {
		copyIDs[k] = v
	}
	return &ComponentChain{mw: c.mw, methodIDs: copyIDs}
}

// anonymousFrame is the frame of a legacy call with no method ID: shared, so
// it costs no allocation (as the empty-string value it replaced did not).
var anonymousFrame frameValue = &componentFrame{}

func (c *ComponentChain) identityContext(ctx context.Context, method string) context.Context {
	// Shadow an outer method's identity (and Op) even for a legacy nested
	// chain.
	f := anonymousFrame
	if id := c.methodID(method); id != "" {
		f = &componentFrame{id: id}
	}
	return context.WithValue(ctx, componentMethodIDKey{}, f)
}
