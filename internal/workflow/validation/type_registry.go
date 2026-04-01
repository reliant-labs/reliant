// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"
	"sync"
)

// typeRegistry maps Go struct types to their field type expectations.
// This is the single source of truth for runtime type validation.
var (
	typeRegistry     = make(map[reflect.Type]map[string]*FieldInfo)
	typeRegistryLock sync.RWMutex
)

// RegisterFieldExpectations registers expected field types for a config struct.
// This should be called in init() near where the struct is defined or used.
func RegisterFieldExpectations(configExample interface{}, expectations map[string]*FieldInfo) {
	typeRegistryLock.Lock()
	defer typeRegistryLock.Unlock()

	t := reflect.TypeOf(configExample)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if _, exists := typeRegistry[t]; exists {
		panic("field expectations already registered for type: " + t.String())
	}

	typeRegistry[t] = expectations
}

// GetExpectedFieldTypeByStruct returns the expected runtime type for a field
// in a config struct. Returns nil if the struct type or field has no registered expectations.
//
// This is used by CEL validation to check if template expressions produce the correct types.
func GetExpectedFieldTypeByStruct(configType reflect.Type, fieldName string) *FieldInfo {
	typeRegistryLock.RLock()
	defer typeRegistryLock.RUnlock()

	if configType.Kind() == reflect.Ptr {
		configType = configType.Elem()
	}

	if expectations, ok := typeRegistry[configType]; ok {
		return expectations[fieldName]
	}
	return nil
}

// GetAllRegisteredTypes returns all types that have field expectations registered.
// Used for testing and debugging.
func GetAllRegisteredTypes() []reflect.Type {
	typeRegistryLock.RLock()
	defer typeRegistryLock.RUnlock()

	types := make([]reflect.Type, 0, len(typeRegistry))
	for t := range typeRegistry {
		types = append(types, t)
	}
	return types
}
