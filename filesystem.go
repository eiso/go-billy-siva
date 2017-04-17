package sivafs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/src-d/go-billy.v2"
	"gopkg.in/src-d/go-billy.v2/subdir"
	"gopkg.in/src-d/go-siva.v1"
)

const (
	defaultBase = "/"
)

var (
	ErrNonSeekableFile = errors.New("file non-seekable")
	ErrAlreadyClosed   = errors.New("file was already closed")
	ErrReadOnlyFile    = errors.New("file is read-only")
	ErrWriteOnlyFile   = errors.New("file is write-only")
)

type sivaFS struct {
	underlying billy.Filesystem
	path       string
	f          billy.File
	rw         *siva.ReadWriter
	lock       *sync.Mutex
}

// New creates a new filesystem backed by a siva file with the given path in
// the given filesystem. The siva file will be opened or created lazily with
// the first operation.
//
// All files opened in write mode must be closed, otherwise the siva file will
// be corrupted.
//
// TempFile is not supported. tmpoverlay should be used if TempFile is needed.
func New(fs billy.Filesystem, path string) billy.Filesystem {
	return &sivaFS{
		underlying: fs,
		path:       path,
		lock:       &sync.Mutex{},
	}
}

// Create creates a new file. This file is created using CREATE, TRUNCATE and
// WRITE ONLY flags due to limitations working on siva files.
func (sfs *sivaFS) Create(path string) (billy.File, error) {
	return sfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0666))
}

func (sfs *sivaFS) Open(path string) (billy.File, error) {
	return sfs.OpenFile(path, os.O_RDONLY, 0)
}

func (sfs *sivaFS) OpenFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	path = normalizePath(path)
	if flag&os.O_CREATE != 0 && flag&os.O_TRUNC == 0 {
		return nil, billy.ErrNotSupported
	}

	sfs.lock.Lock()
	defer sfs.lock.Unlock()

	if err := sfs.ensureOpen(); err != nil {
		return nil, err
	}

	if flag&os.O_CREATE != 0 {
		return sfs.createFile(path, flag, mode)
	}

	return sfs.openFile(path, flag, mode)
}

func (sfs *sivaFS) Stat(p string) (billy.FileInfo, error) {
	p = normalizePath(p)

	sfs.lock.Lock()
	defer sfs.lock.Unlock()

	if err := sfs.ensureOpen(); err != nil {
		return nil, err
	}

	index, err := sfs.getIndex()
	if err != nil {
		return nil, err
	}

	e := index.Find(p)
	if e != nil {
		return newFileInfo(e), nil
	}

	stat, err := getDir(index, p)
	if err != nil {
		return nil, err
	}

	if stat == nil {
		return nil, os.ErrNotExist
	}

	return stat, nil
}

func (sfs *sivaFS) ReadDir(path string) ([]billy.FileInfo, error) {
	path = normalizePath(path)

	sfs.lock.Lock()
	defer sfs.lock.Unlock()

	if err := sfs.ensureOpen(); err != nil {
		return nil, err
	}

	index, err := sfs.getIndex()
	if err != nil {
		return nil, err
	}

	files, err := listFiles(index, path)
	if err != nil {
		return nil, err
	}

	dirs, err := listDirs(index, path)
	if err != nil {
		return nil, err
	}

	return append(dirs, files...), nil
}

func (sfs *sivaFS) MkdirAll(filename string, perm os.FileMode) error {
	sfs.lock.Lock()
	defer sfs.lock.Unlock()

	if err := sfs.ensureOpen(); err != nil {
		return err
	}

	index, err := sfs.getIndex()
	if err != nil {
		return err
	}
	e := index.Find(filename)
	if e != nil {
		return &os.PathError{
			Op:   "mkdir",
			Path: filename,
			Err:  syscall.ENOTDIR,
		}
	}

	return nil
}

// Join joins the specified elements using the filesystem separator.
func (sfs *sivaFS) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (sfs *sivaFS) Dir(path string) billy.Filesystem {
	return subdir.New(sfs, sfs.Join(sfs.Base(), path))
}

func (sfs *sivaFS) Base() string {
	return defaultBase
}

func (sfs *sivaFS) Remove(path string) error {
	path = normalizePath(path)

	sfs.lock.Lock()
	defer sfs.lock.Unlock()

	if err := sfs.ensureOpen(); err != nil {
		return err
	}

	index, err := sfs.getIndex()
	if err != nil {
		return err
	}

	e := index.Find(path)

	if e != nil {
		return sfs.rw.WriteHeader(&siva.Header{
			Name:    path,
			ModTime: time.Now(),
			Mode:    0,
			Flags:   siva.FlagDeleted,
		})
	}

	dir, err := getDir(index, path)
	if err != nil {
		return err
	}

	if dir != nil {
		return &os.PathError{
			Op:   "remove",
			Path: path,
			Err:  syscall.ENOTEMPTY,
		}
	}

	// there are no file and no directory with this path
	return os.ErrNotExist
}

func (sfs *sivaFS) Rename(from, to string) error {
	return billy.ErrNotSupported
}

func (sfs *sivaFS) TempFile(dir string, prefix string) (billy.File, error) {
	return nil, billy.ErrNotSupported
}

func (sfs *sivaFS) Close() error {
	return sfs.ensureClosed()
}

func (sfs *sivaFS) ensureOpen() error {
	if sfs.rw != nil {
		return nil
	}

	f, err := sfs.underlying.OpenFile(sfs.path, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return err
	}

	rw, err := siva.NewReaderWriter(f)
	if err != nil {
		f.Close()
		return err
	}

	sfs.rw = rw
	sfs.f = f
	return nil
}

func (sfs *sivaFS) ensureClosed() error {
	if sfs.rw == nil {
		return nil
	}

	err := sfs.rw.Close()
	if err != nil {
		return err
	}

	sfs.rw = nil
	f := sfs.f
	sfs.f = nil
	return f.Close()
}

func (sfs *sivaFS) createFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	if flag&os.O_RDWR != 0 || flag&os.O_RDONLY != 0 {
		return nil, billy.ErrNotSupported
	}

	header := &siva.Header{
		Name:    path,
		Mode:    mode,
		ModTime: time.Now(),
	}
	err := sfs.rw.WriteHeader(header)
	if err != nil {
		return nil, err
	}

	closeFunc := func() error {
		return sfs.rw.Flush()
	}

	return newFile(path, sfs.rw, sfs.lock, closeFunc), nil
}

func (sfs *sivaFS) openFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	if flag&os.O_RDWR != 0 || flag&os.O_WRONLY != 0 {
		return nil, billy.ErrNotSupported
	}

	index, err := sfs.getIndex()
	if err != nil {
		return nil, err
	}

	e := index.Find(path)
	if e == nil {
		return nil, os.ErrNotExist
	}

	sr, err := sfs.rw.Get(e)
	if err != nil {
		return nil, err
	}

	return openFile(path, sr, sfs.lock), nil
}

func (sfs *sivaFS) getIndex() (siva.Index, error) {
	index, err := sfs.rw.Index()
	if err != nil {
		return nil, err
	}

	return index.Filter(), nil
}

func listFiles(index siva.Index, dir string) ([]billy.FileInfo, error) {
	dir = addTrailingSlash(dir)

	entries, err := index.Glob(fmt.Sprintf("%s*", dir))
	if err != nil {
		return nil, err
	}

	contents := []billy.FileInfo{}
	for _, e := range entries {
		contents = append(contents, newFileInfo(e))
	}

	return contents, nil
}

func getDir(index siva.Index, dir string) (billy.FileInfo, error) {
	dir = addTrailingSlash(dir)

	entries, err := index.Glob(path.Join(dir, "*"))
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	var oldDir time.Time
	for _, e := range entries {
		if oldDir.Before(e.ModTime) {
			oldDir = e.ModTime
		}
	}

	return newDirFileInfo(path.Clean(dir), oldDir), nil
}

func listDirs(index siva.Index, dir string) ([]billy.FileInfo, error) {
	dir = addTrailingSlash(dir)

	entries, err := index.Glob(fmt.Sprintf("%s*/*", dir))
	if err != nil {
		return nil, err
	}

	dirs := map[string]time.Time{}
	for _, e := range entries {
		dir := filepath.Dir(e.Name)
		oldDir, ok := dirs[dir]
		if !ok || oldDir.Before(e.ModTime) {
			dirs[dir] = e.ModTime
		}
	}

	contents := []billy.FileInfo{}
	for dir, mt := range dirs {
		contents = append(contents, newDirFileInfo(dir, mt))
	}

	return contents, nil
}

// addTrailingSlash adds trailing slash to the path if it does not have one.
func addTrailingSlash(path string) string {
	if path == "" {
		return path
	}

	if !strings.HasSuffix(path, "/") {
		path = path + "/"
	}

	return path
}

// normalizePath returns a path relative to '/'.
// It assumes UNIX-style slash-delimited paths.
func normalizePath(path string) string {
	path = filepath.Join("/", path)
	return removeLeadingSlash(path)
}

// removeLeadingSlash removes leading slash of the path, if any.
func removeLeadingSlash(path string) string {
	if strings.HasPrefix(path, "/") {
		return path[1:]
	}

	return path
}
