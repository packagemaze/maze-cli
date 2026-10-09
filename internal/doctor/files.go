package doctor

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

const (
	maxConfigFileBytes = 1 << 20
	maxAncestorDepth   = 32
	maxMatchingFiles   = 64
)

// project is the directory being diagnosed and its ancestors up to the
// repository root, nearest first, so a package inside a monorepo finds the
// .npmrc committed at the top. Every read goes through an os.Root confined to
// that root: a symlink cannot lead outside the repository, and fs.FS can only
// read.
type project struct {
	dir     string
	root    *os.Root
	fsys    fs.FS
	dirs    []string
	skipped []string
	notes   []NotChecked
}

func discoverProject(dir string) (*project, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "doctor", Path: dir, Err: fs.ErrInvalid}
	}
	rootPath, found := repositoryRoot(absolute)
	p := &project{dir: absolute}
	if !found {
		rootPath = absolute
		p.notes = append(p.notes, NotChecked{Check: "repository_root", Reason: fmt.Sprintf("No .git marker above %s, so only that directory was read.", absolute)})
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	p.root = root
	p.fsys = root.FS()
	for current := absolute; ; current = filepath.Dir(current) {
		relative, err := filepath.Rel(rootPath, current)
		if err != nil {
			break
		}
		p.dirs = append(p.dirs, filepath.ToSlash(relative))
		if current == rootPath {
			break
		}
	}
	return p, nil
}

func repositoryRoot(dir string) (string, bool) {
	current := dir
	for depth := 0; depth < maxAncestorDepth; depth++ {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", false
}

func (p *project) close() {
	_ = p.root.Close()
}

type projectFile struct {
	path    string
	content string
}

type readOutcome int

const (
	readMissing readOutcome = iota
	readSkipped
	readOK
)

// nearest returns the first file called name walking from the diagnosed
// directory up to the repository root. A nearer copy that cannot be read is
// the one in effect, so the walk stops there rather than reporting an
// ancestor's file in its place.
func (p *project) nearest(name string) (projectFile, bool) {
	for _, dir := range p.dirs {
		file, outcome := p.read(path.Join(dir, name))
		if outcome != readMissing {
			return file, outcome == readOK
		}
	}
	return projectFile{}, false
}

// nearestMatching returns the files in the nearest directory that holds at
// least one file accepted by match.
func (p *project) nearestMatching(match func(name string) bool) []projectFile {
	for _, dir := range p.dirs {
		if files, found := p.matching(dir, match); found {
			return files
		}
	}
	return nil
}

func (p *project) matching(dir string, match func(name string) bool) ([]projectFile, bool) {
	entries, err := fs.ReadDir(p.fsys, dir)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && match(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil, false
	}
	sort.Strings(names)
	if len(names) > maxMatchingFiles {
		p.notes = append(p.notes, NotChecked{Check: "file_count", Reason: fmt.Sprintf("%s holds %d matching files; only the first %d were read.", dir, len(names), maxMatchingFiles)})
		names = names[:maxMatchingFiles]
	}
	files := make([]projectFile, 0, len(names))
	for _, name := range names {
		if file, outcome := p.read(path.Join(dir, name)); outcome == readOK {
			files = append(files, file)
		}
	}
	return files, true
}

// read bounds every file at maxConfigFileBytes on the read itself, not only on
// the size stat reports, so a special file or a growing one cannot exhaust
// memory.
func (p *project) read(filePath string) (projectFile, readOutcome) {
	info, err := fs.Stat(p.fsys, filePath)
	if err != nil {
		return projectFile{}, readMissing
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigFileBytes {
		return p.skip(filePath)
	}
	file, err := p.fsys.Open(filePath)
	if err != nil {
		return projectFile{}, readMissing
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxConfigFileBytes+1))
	if err != nil || len(content) > maxConfigFileBytes {
		return p.skip(filePath)
	}
	return projectFile{path: filePath, content: string(content)}, readOK
}

func (p *project) skip(filePath string) (projectFile, readOutcome) {
	p.skipped = append(p.skipped, filePath)
	return projectFile{}, readSkipped
}

func (p *project) exists(name string) bool {
	for _, dir := range p.dirs {
		if _, err := fs.Stat(p.fsys, path.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
