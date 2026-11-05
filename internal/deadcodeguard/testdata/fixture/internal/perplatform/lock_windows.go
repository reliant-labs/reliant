package perplatform

// lock is a stub on Windows: this platform's honest answer, while Unix does
// real work under the same name.
func lock(int) error { return nil }

// probe is a stub on Windows as well, so it is a stub everywhere.
func probe(string) error { return nil }
