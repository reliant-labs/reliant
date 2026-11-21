package chat

import (
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// maxAppNameLength bounds a chat's app tag. forge sets no length on a
// project name; this is reliant's, and chats_app_name_valid holds it too.
const maxAppNameLength = 128

// validAppName reports whether name has forge's project-name shape (forge
// internal/cli/cmdutil.ValidateProjectName): a letter first, then letters,
// digits, '_' or '-'. forge also refuses Go keywords there, for the Go
// identifiers it derives from a new project's name; that is a codegen rule,
// not part of an app's identity, so it is not repeated here. The
// chats_app_name_valid CHECK holds the same shape for ASCII and admits any
// non-ASCII character, so it never refuses a name this accepts.
func validAppName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxAppNameLength || !utf8.ValidString(name) {
		return false
	}
	for i, r := range name {
		switch {
		case unicode.IsLetter(r):
		case i == 0:
			return false
		case unicode.IsDigit(r), r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseAppName is StartChatRequest.app_name as the launch takes it: nil when
// unset or empty, INVALID_ARGUMENT when it is not a forge app name. It is
// checked even when chat_id names an existing chat, which ignores it.
func parseAppName(name *string) (*string, error) {
	if name == nil || *name == "" {
		return nil, nil
	}
	if !validAppName(*name) {
		return nil, svcerr.InvalidArgument("app_name must be a forge app name: a letter, then letters, digits, '_' or '-', at most 128 characters")
	}
	app := *name
	return &app, nil
}

// appNameFilter is ListChatsRequest.app_name as a filter: nil when unset or
// empty. A malformed name is not refused; no chat carries one, so it lists
// nothing.
func appNameFilter(name *string) *string {
	if name == nil || *name == "" {
		return nil
	}
	app := *name
	return &app
}
