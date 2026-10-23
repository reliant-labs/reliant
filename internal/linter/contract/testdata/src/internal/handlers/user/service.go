package user

// Handler packages under internal/handlers/ implement the proto Connect
// service interface. Their exported RPC methods (GetCurrentUser, ...) are the
// proto boundary, not a hand-written Go contract — so no contract.go is
// required here. Zero findings expected.

type Service struct{}

func (s *Service) GetCurrentUser() error { return nil }

func (s *Service) UpdateProfile() error { return nil }
