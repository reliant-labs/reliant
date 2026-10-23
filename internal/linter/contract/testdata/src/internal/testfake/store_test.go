package testfake

// A test file is not the package's API: it compiles only into the test
// binary, so nothing that imports the package can reach what it declares.
// No finding is expected anywhere in this file.

// memStore is a test fake that happens to satisfy BindingStore. Its other
// exported methods are test conveniences, not a second surface beside the
// contract.
type memStore struct{ m map[string]string }

func (s *memStore) Get(key string) (string, bool) {
	v, ok := s.m[key]
	return v, ok
}

func (s *memStore) Put(key, value string) { s.m[key] = value }

func (s *memStore) Reset() { s.m = map[string]string{} }

func (s *memStore) Len() int { return len(s.m) }

func (s *memStore) Seed(kv map[string]string) {
	for k, v := range kv {
		s.m[k] = v
	}
}

// The export_test.go idiom: an exported method on the package's OWN type,
// declared in a test file so the package's external tests can reach an
// internal. It exists only in the test binary.
func (s *pgStore) ExportedForTest() int { return 0 }
