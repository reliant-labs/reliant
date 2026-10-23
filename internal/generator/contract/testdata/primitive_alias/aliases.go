package primitive_alias

// SiblingTag is a named string declared in a sibling .go file (not
// contract.go). The mock generator must scan sibling files to discover
// it as a primitive alias so a contract method returning SiblingTag
// produces `""` instead of the invalid `SiblingTag{}`.
type SiblingTag string
