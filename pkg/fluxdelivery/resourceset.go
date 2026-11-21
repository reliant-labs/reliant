package fluxdelivery

import "fmt"

// resourceSetLabels are the labels the ResourceSet object itself carries; the
// same map as kcl/lib/flux_delivery.k's _LABELS.
func resourceSetLabels() map[string]any {
	return map[string]any{"app.kubernetes.io/name": "flux-delivery", "app.kubernetes.io/managed-by": "forge"}
}

// ResourceSet is the ResourceSet that renders one binding, with the binding's
// input inline — the hub's shape, where the writer owns the object. It is the
// object kcl's `flux_delivery_resourceset(ResourceSetName(env, binding),
// namespace, inputs = [in.Map()])` returns; the KCL agreement test pins the
// two.
//
// It validates nothing: call [Validate] first. It refuses only an input it
// cannot encode.
func ResourceSet(in BindingInput) (map[string]any, error) {
	input, err := in.Map()
	if err != nil {
		return nil, err
	}
	if in.Namespace == "" || in.Env == "" {
		return nil, fmt.Errorf("%w: a ResourceSet needs the input's env and namespace", ErrInvalidInput)
	}
	return map[string]any{
		"apiVersion": APIVersion,
		"kind":       KindResourceSet,
		"metadata": map[string]any{
			"name":      ResourceSetName(in.Env, in.Binding),
			"namespace": in.Namespace,
			"labels":    resourceSetLabels(),
		},
		"spec": map[string]any{
			"serviceAccountName": ServiceAccount,
			"inputs":             []any{input},
			"resourcesTemplate":  Template,
		},
	}, nil
}
