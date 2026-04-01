// Package localfs provides an abstraction over local filesystem operations.
// In the monolith, Local wraps the standard os/filepath calls.
// In the split architecture, a remote implementation will proxy through the tools daemon.
package localfs

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FS abstracts filesystem operations so they can be backed by the local OS
// or proxied through the tools-daemon gRPC service.
type FS interface {
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	MkdirAll(path string, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	Remove(name string) error
	RemoveAll(path string) error
	Open(name string) (io.ReadCloser, error)
	Walk(root string, fn filepath.WalkFunc) error
	IsNotExist(err error) bool
}

// Local implements FS using the standard library.
type Local struct{}

// New returns a Local filesystem implementation.
func New() *Local { return &Local{} }

func (*Local) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (*Local) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }
func (*Local) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (*Local) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (*Local) Stat(name string) (fs.FileInfo, error)        { return os.Stat(name) }
func (*Local) Remove(name string) error                     { return os.Remove(name) }
func (*Local) RemoveAll(path string) error                  { return os.RemoveAll(path) }
func (*Local) Open(name string) (io.ReadCloser, error)      { return os.Open(name) }
func (*Local) Walk(root string, fn filepath.WalkFunc) error { return filepath.Walk(root, fn) }
func (*Local) IsNotExist(err error) bool                    { return os.IsNotExist(err) }
