package observe

import "context"

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

func (c *ComponentChain) identityContext(ctx context.Context, method string) context.Context {
	// Shadow an outer method's identity even for a legacy nested chain.
	id := ""
	if c != nil {
		id = c.methodIDs[method]
	}
	return context.WithValue(ctx, componentMethodIDKey{}, id)
}
