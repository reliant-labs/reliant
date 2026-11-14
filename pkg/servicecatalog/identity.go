package servicecatalog

// MethodIdentity is the join metadata shared by the catalog and generated
// decorators. Owner is the exposed interface/receiver whose method set is being
// decorated, NOT the receiver declaring an inherited/embedded method. Namespace
// is the actual instrumentation namespace (normally the Go package clause), not
// an import-path basename. Neither signature nor declaration location enters ID.
func MethodIdentity(scope, owner, namespace, method string) (id, legacyName string) {
	return MethodID(scope, owner, method), namespace + "." + method
}
