package bad

type Service interface {
	GetItem(id string) (string, error)
	CreateItem(name string) error
}
