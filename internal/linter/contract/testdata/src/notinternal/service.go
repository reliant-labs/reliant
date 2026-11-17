package notinternal

// Not under internal/ — requirecontract should skip entirely.

type myService struct{}

func (s *myService) DoWork() error {
	return nil
}
