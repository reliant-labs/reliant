package naming

import "testing"

// The ORM's field names are Go's: initialisms in capitals (reliant P5b,
// F12.6). They were Id / UserId, which golint and every Go reader flag.
func TestColumnGoName_GoInitialisms(t *testing.T) {
	for col, want := range map[string]string{
		"id":             "ID",
		"org_id":         "OrgID",
		"user_ids":       "UserIds", // a plural is not an initialism (ST1003)
		"api_key":        "APIKey",
		"http_url":       "HTTPURL",
		"uuid":           "UUID",
		"identity":       "Identity",
		"address_line_2": "AddressLine2",
		"base64item":     "Base64item",
		"chat_id_seq":    "ChatIDSeq",
		"idx":            "Idx",
	} {
		if got := ColumnGoName(col); got != want {
			t.Errorf("ColumnGoName(%q) = %q, want %q", col, got, want)
		}
	}
}
