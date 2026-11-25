package testfake

type pgStore struct{}

// New returns the package's real implementation.
func New() BindingStore { return &pgStore{} }

func (s *pgStore) Get(key string) (string, bool) { return "", false }

func (s *pgStore) Put(key, value string) {}

// Rogue is on the package's API, so the single-seam rule still fires: the
// test-file skip is per FILE, not per package.
func (s *pgStore) Rogue() {} // want `exported method Rogue on type pgStore is not declared in the BindingStore interface \(contract.go\)`
