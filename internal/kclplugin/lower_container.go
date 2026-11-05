package kclplugin

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// lowerContainer decodes the fw.Container KCL passed (strictly: a misspelled
// field must fail the render, not silently lower to a default) and returns
// its Kubernetes container.
func lowerContainer(raw any) (any, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var c v1alpha1.Container
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("lower_container: %w", err)
	}
	return deploy.LowerContainer(c), nil
}
