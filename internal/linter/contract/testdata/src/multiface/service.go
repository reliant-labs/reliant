package multiface

// svc implements Service — only Serve() allowed.
type svc struct{}

func (s *svc) Serve() error { return nil }

func (s *svc) BadServeHelper() {} // want `exported method BadServeHelper on type svc is not declared in the Service interface \(contract.go\)`

// repo implements Repository — only Get/Put allowed.
type repo struct{}

func (r *repo) Get(id string) (string, error)   { return "", nil }
func (r *repo) Put(id string, val string) error { return nil }

func (r *repo) Flush() error { return nil } // want `exported method Flush on type repo is not declared in the Repository interface \(contract.go\)`
