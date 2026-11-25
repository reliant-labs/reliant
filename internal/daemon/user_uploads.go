package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// User uploads: a staged upload whose destination is the machine's uploads
// folder, outside every workspace, instead of a workspace folder.
//
// The chat composer sends a file it cannot attach (a spreadsheet, an archive,
// a text file over the attachment cap) here, and points the agent at the
// file's absolute path. Keeping it out of the repo means no stray file in git
// status for the agent to commit.
//
// The daemon resolves the root itself, from the home directory of the user
// it runs as: ~/.reliant/uploads/<folder>/<filename>. The request names only
// the folder (a chat id, or a new chat's draft id) and the file's base name,
// both refused unless they are a single, plain path element, so no caller can
// name another location. A name already taken there is kept, and the upload
// takes the first free "name (n).ext": chat never asks.
//
// Only the begin is a command of its own (UserUploadBeginCommand). Chunk,
// commit, abort and status are the staged-upload commands, unchanged on the
// wire: the session records its destination, and a daemon that never had
// this command never has such a session. A daemon that predates it answers
// the begin with the registry's "older version of Reliant" error
// (daemonerr.IsOutdated), which the server reports as "update Reliant on
// that machine"; there is no whole-file fallback, since no older command may
// write outside a workspace.
const UserUploadBeginCommand = "fs.user_upload_begin"

// UserUploadsDir is the uploads folder, relative to the daemon user's home
// directory. Its .partial subfolder (UploadPartialDir) is the staging area of
// every upload, and no uploads folder name can reach it (ValidUserUploadsFolder).
const UserUploadsDir = ".reliant/uploads"

// UploadDestinationUserUploads marks a session, and its commit reply, as a
// user upload ("destination"). Absent is a workspace upload.
const UploadDestinationUserUploads = "user_uploads"

// maxUserUploadsFolder bounds an uploads folder name: a UUID is 36.
const maxUserUploadsFolder = 128

// maxUploadNameBytes is the longest file name most filesystems allow.
const maxUploadNameBytes = 255

// UserUploadBeginRequest is fs.user_upload_begin's payload. Its reply is
// UploadBeginResponse.
type UserUploadBeginRequest struct {
	// Root is the workspace root the request was scoped by. It is not where
	// the file goes: the session is bound to it, as every staged session is,
	// so its later commands must name the same root.
	Root string `json:"root"`
	// Folder is the folder under UserUploadsDir (ValidUserUploadsFolder).
	Folder string `json:"folder"`
	// Filename is the file's base name (ValidateUploadFilename).
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	// MaxSize caps Size; 0 means DefaultUploadMaxSize.
	MaxSize int64 `json:"max_size,omitempty"`
}

// ValidUserUploadsFolder reports whether name may be a folder under
// UserUploadsDir: 1 to 128 ASCII letters, digits, '-' or '_', starting with a
// letter or digit. So it is one path element, never "." or "..", never hidden
// (the staging folder is .partial), and the same on every filesystem.
func ValidUserUploadsFolder(name string) bool {
	if name == "" || len(name) > maxUserUploadsFolder {
		return false
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// ValidateUploadFilename refuses a file name that is not one plain path
// element: empty, "." or "..", a separator of either convention (the daemon
// may run Windows), a NUL or other control character, or longer than a
// filesystem takes. A refusal wraps ErrUploadRefusedPath.
func ValidateUploadFilename(name string) error {
	switch {
	case name == "":
		return refusedPath("a file name is required")
	case name == "." || name == "..":
		return refusedPath("%q is not a file name", name)
	case len(name) > maxUploadNameBytes:
		return refusedPath("the file name is longer than %d bytes", maxUploadNameBytes)
	case strings.ContainsAny(name, `/\`):
		return refusedPath("the file name %q has a path separator", name)
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return refusedPath("the file name has a control character")
		}
	}
	return nil
}

// UserUploadsRoot is the uploads folder of the user the process runs as:
// ~/.reliant/uploads, absolute.
func UserUploadsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory for uploads: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(UserUploadsDir)), nil
}

// ResolveUserUploadDest is where a user upload of filename into folder
// lands under uploadsRoot (UserUploadsRoot): uploadsRoot/folder/filename,
// cleaned. Both names must be valid, and the destination must stay below
// uploadsRoot once symlinks are resolved (ValidateUploadDest), so a folder
// that is a symlink out of the uploads folder cannot carry the file away.
// Every refusal wraps ErrUploadRefusedPath.
func ResolveUserUploadDest(uploadsRoot, folder, filename string) (string, error) {
	if !ValidUserUploadsFolder(folder) {
		return "", refusedPath("%q is not an uploads folder name", folder)
	}
	if err := ValidateUploadFilename(filename); err != nil {
		return "", err
	}
	if !filepath.IsAbs(uploadsRoot) {
		return "", refusedPath("the uploads folder must be an absolute path")
	}
	return ValidateUserUploadDest(uploadsRoot, filepath.Join(uploadsRoot, folder, filename))
}

// ValidateUserUploadDest checks a user upload's destination again, as a
// commit must (the folder may have been swapped for a symlink since the
// begin): dest must be a file exactly two levels below uploadsRoot, in a
// valid uploads folder, and stay below uploadsRoot once symlinks are
// resolved. It returns dest cleaned.
func ValidateUserUploadDest(uploadsRoot, dest string) (string, error) {
	uploadsRoot = filepath.Clean(uploadsRoot)
	dest = filepath.Clean(dest)
	rel, ok := relBelow(uploadsRoot, dest)
	if !ok {
		return "", refusedPath("%s is outside the uploads folder", dest)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || !ValidUserUploadsFolder(parts[0]) {
		return "", refusedPath("%s is not a file of an uploads folder", dest)
	}
	if err := ValidateUploadFilename(parts[1]); err != nil {
		return "", err
	}
	return ValidateUploadDest(uploadsRoot, dest)
}
