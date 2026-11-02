package toolexec

// ContextMap is the server-built context a daemon receives with the request,
// the map sent over the wire as the request's context_json.
func (req *ToolRequest) ContextMap() map[string]interface{} { return req.toolContextMap() }
