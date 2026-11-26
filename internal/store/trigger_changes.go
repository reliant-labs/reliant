// Copyright (c) 2025 Reliant Labs
package store

func nonNil(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
