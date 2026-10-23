// Package testcontainers is a stand-in for testcontainers-go.
package testcontainers

type Container struct{}

func Run(image string) (*Container, error) { return &Container{}, nil }
