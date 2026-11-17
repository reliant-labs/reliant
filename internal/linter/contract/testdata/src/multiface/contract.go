package multiface

type Service interface {
	Serve() error
}

type Repository interface {
	Get(id string) (string, error)
	Put(id string, val string) error
}
