package schema

import "github.com/reliant-labs/reliant/internal/integrations/manifest"

// The rules the schema adds to the message structure, keyed "Message.field".
// Every value comes from the manifest package's exported rules, the ones its
// validators enforce, so the two cannot differ.

// hidden fields are not authored: the loader derives them.
var hidden = map[string]bool{"ActionSpec.param_order": true}

var required = map[string][]string{
	"IntegrationManifest": {"id", "version"},
	"ActionSpec":          {"id", "placement"},
	"TriggerSpec":         {"id", "events", "data"},
	"TriggerAttribute":    {"name"},
	"ConnectionParam":     {"name"},
}

var enums = map[string][]string{
	"ActionSpec.kind":                 {"call"},
	"ActionSpec.placement":            manifest.Placements,
	"OAuth2Auth.scope_separator":      manifest.ScopeSeparators,
	"OAuth2Auth.pkce":                 manifest.PKCEMethods,
	"RevokeSpec.method":               manifest.RevokeMethods,
	"RevokeSpec.client_auth":          manifest.RevokeClientAuths,
	"RevokeSpec.token_in":             manifest.RevokeTokenLocation,
	"ApiKeyAuth.in":                   manifest.AuthLocations,
	"IdentityProbe.method":            manifest.ProbeMethods,
	"PaginationSpec.style":            manifest.PaginationStyles,
	"HttpRequestSpec.method":          manifest.HTTPMethods,
	"HttpRequestSpec.body_format":     manifest.BodyFormats,
	"HttpRequestSpec.response_format": manifest.ResponseFormats,
	"HttpRequestSpec.redirects":       manifest.RedirectModes,
}

// templatable enum fields also accept a {{ template }}.
var templatable = map[string]bool{
	"HttpRequestSpec.method":          true,
	"HttpRequestSpec.body_format":     true,
	"HttpRequestSpec.response_format": true,
	"HttpRequestSpec.redirects":       true,
}

var patterns = map[string]string{
	"IntegrationManifest.id": manifest.IDPattern.String(),
	"ActionSpec.id":          manifest.ActionIDPattern.String(),
	"TriggerSpec.id":         manifest.ActionIDPattern.String(),
	"ActionSpec.executor":    manifest.ExecutorPattern.String(),
	"ToolSpec.name":          manifest.ToolNamePattern.String(),
	"TriggerAttribute.name":  manifest.AttributeNamePattern.String(),
	"ConnectionParam.name":   manifest.ParamNamePattern.String(),
	"DelegatedAuth.broker":   manifest.BrokerPattern.String(),
	"TriggerSpec.events":     manifest.EventTypePattern.String(),
}

var maxLength = map[string]int{
	"ActionSpec.summary":  200,
	"TriggerSpec.summary": 200,
}

var maxItems = map[string]int{
	"TriggerSpec.events":     manifest.MaxTriggerEvents,
	"TriggerSpec.attributes": manifest.MaxTriggerAttributes,
}

var minItems = map[string]int{"TriggerSpec.events": 1}

var bounds = map[string][2]int64{
	"IntegrationManifest.version":        {1, 1<<31 - 1},
	"HttpRequestSpec.timeout_seconds":    {0, manifest.MaxTimeoutSeconds},
	"HttpRequestSpec.max_response_bytes": {0, manifest.MaxResponseBytesLimit},
	"PaginationSpec.max_pages":           {0, manifest.MaxPaginationPages},
}

var keywordItems = map[string]bool{
	"IntegrationManifest.keywords": true,
	"ActionSpec.keywords":          true,
	"TriggerSpec.keywords":         true,
}

// jsonSchemaFields hold a JSON Schema whose root is an object.
var jsonSchemaFields = map[string]bool{
	"ActionSpec.params": true,
	"TriggerSpec.data":  true,
}
