package clientconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Filesystem is what a Workspace needs from the operating system.
type Filesystem struct {
	// MkdirTemp creates a new directory only its owner can open.
	MkdirTemp func(dir string, pattern string) (string, error)
	// CreateFile creates a new file with perm, failing if it exists.
	CreateFile func(name string, content []byte, perm fs.FileMode) error
	RemoveAll  func(path string) error
}

// OSFilesystem is the operating system's filesystem. os.MkdirTemp creates
// directories with mode 0700.
func OSFilesystem() Filesystem {
	return Filesystem{MkdirTemp: os.MkdirTemp, CreateFile: createFile, RemoveAll: os.RemoveAll}
}

func createFile(name string, content []byte, perm fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	return errors.Join(writeErr, file.Close())
}

// Workspace is a fresh private directory holding one command's client files.
type Workspace struct {
	dir string
	fs  Filesystem
}

// NewWorkspace creates the directory under the system temporary directory.
func NewWorkspace(filesystem Filesystem) (*Workspace, error) {
	dir, err := filesystem.MkdirTemp("", "maze-exec-")
	if err != nil {
		return nil, err
	}
	return &Workspace{dir: dir, fs: filesystem}, nil
}

// Dir is the directory's path.
func (workspace *Workspace) Dir() string {
	return workspace.dir
}

// Write creates a file only its owner can read.
func (workspace *Workspace) Write(name string, content []byte) (string, error) {
	path := filepath.Join(workspace.dir, filepath.Base(name))
	return path, workspace.fs.CreateFile(path, content, 0o600)
}

// Close removes the directory and everything in it.
func (workspace *Workspace) Close() error {
	return workspace.fs.RemoveAll(workspace.dir)
}
