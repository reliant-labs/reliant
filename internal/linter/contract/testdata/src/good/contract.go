package good

type Service interface {
	GetItem(id string) (string, error)
	CreateItem(name string) error
	DeleteItem(id string) error
}
