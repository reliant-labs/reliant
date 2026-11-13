package nomethods

// ExportedType is fine — only methods on implementing types are checked.
type ExportedType struct {
	Name string
}

// ExportedFunction is fine — standalone functions are not checked.
func ExportedFunction() string {
	return "ok"
}

// ExportedConstant is fine.
const ExportedConstant = "hello"

// ExportedVar is fine.
var ExportedVar = 42

type worker struct{}

func NewWorker() Service {
	return &worker{}
}

func (w *worker) DoWork() error {
	return nil
}
