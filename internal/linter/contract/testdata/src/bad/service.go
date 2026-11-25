package bad

type myService struct{}

func New() Service {
	return &myService{}
}

func (s *myService) GetItem(id string) (string, error) {
	return "", nil
}

func (s *myService) CreateItem(name string) error {
	return nil
}

func (s *myService) ExtraMethod() string { // want `exported method ExtraMethod on type myService is not declared in the Service interface \(contract.go\)`
	return "bad"
}

func (s *myService) AnotherExtra(x int) error { // want `exported method AnotherExtra on type myService is not declared in the Service interface \(contract.go\)`
	return nil
}

// unexported is fine
func (s *myService) internalHelper() {}
