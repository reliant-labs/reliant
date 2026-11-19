package derived

//forge:contract
type service struct{}

func (s *service) Do() error { return nil }

func (s *service) Extra() {} // want "exported method Extra on type service is not declared in the Service interface"
