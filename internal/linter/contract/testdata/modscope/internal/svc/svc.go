// Package svc is in the module's root internal/ tree: the rule applies.
package svc // want "package svc has exported methods but no contract.go"

type Service struct{}

func (s *Service) DoWork() error { return nil }
