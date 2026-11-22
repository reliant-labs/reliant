package stripeadp

import (
	"context"
	"net/http"
)

type service struct {
	http *http.Client
}

func New() Service {
	return &service{http: http.DefaultClient}
}

func (s *service) HealthCheck(_ context.Context) error {
	return nil
}
