// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: ContentPart is a sealed sum type (unexported isPart marker) over the message part structs, not interchangeable services; a contract.go adds nothing; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: LLM message/content-part vocabulary; ContentPart is a sealed sum type, not a service
package message

type Attachment struct {
	FilePath string
	FileName string
	MimeType string
	Content  []byte
}
