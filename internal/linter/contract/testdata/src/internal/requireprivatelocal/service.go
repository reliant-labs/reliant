package requireprivatelocal // want "package requireprivatelocal has exported methods but no contract.go"

import "io"

// Service is the package's own exported interface. An unexported type that
// implements it is the package's behavioral surface, which belongs in
// contract.go — even though Close also satisfies io.Closer.
type Service interface {
	Do() error
	Close() error
}

type service struct{}

func (s *service) Do() error    { return nil }
func (s *service) Close() error { return nil }

var _ io.Closer = (*service)(nil)

// New returns the Service.
func New() Service { return &service{} }
