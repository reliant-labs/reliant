package requirebad // want "package requirebad has exported methods but no contract.go"

type myService struct{}

func (s *myService) DoWork() error {
	return nil
}
