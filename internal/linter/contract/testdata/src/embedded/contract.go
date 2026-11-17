package embedded

type Base interface {
	Start() error
}

type Service interface {
	Base
	Stop() error
}
