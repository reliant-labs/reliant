package requirenotorm // want "package requirenotorm has exported methods but no contract.go"

func (s *store) Save() error { return nil }
