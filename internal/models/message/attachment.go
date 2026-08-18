// forge:exclude-contract
//
// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
package message

type Attachment struct {
	FilePath string
	FileName string
	MimeType string
	Content  []byte
}
