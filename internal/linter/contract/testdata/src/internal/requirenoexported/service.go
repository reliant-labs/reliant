package requirenoexported

// No exported methods on any struct — no contract needed.

type myService struct{}

func (s *myService) doWork() error {
	return nil
}

func ExportedFunc() {} // funcs don't count, only methods
