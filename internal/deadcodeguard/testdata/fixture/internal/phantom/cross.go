package phantom

// Cross is read here and written only from package consumer.
type Cross struct {
	// OK: the writer lives in another package, which only a whole-program
	// scan can see.
	CrossWritten string
}

// Read returns the cross-package-written field.
func (c Cross) Read() string { return c.CrossWritten }
