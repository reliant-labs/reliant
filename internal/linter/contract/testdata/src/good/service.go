package good

type myService struct {
	store map[string]string
}

func New() Service {
	return &myService{store: make(map[string]string)}
}

func (s *myService) GetItem(id string) (string, error) {
	return s.store[id], nil
}

func (s *myService) CreateItem(name string) error {
	s.store[name] = name
	return nil
}

func (s *myService) DeleteItem(id string) error {
	delete(s.store, id)
	return nil
}

// unexported methods are fine
func (s *myService) helper() string {
	return "ok"
}
