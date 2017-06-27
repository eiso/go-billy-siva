package sivafs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/src-d/go-billy.v3"
	"gopkg.in/src-d/go-billy.v3/helper/chroot"
	"gopkg.in/src-d/go-billy.v3/helper/mount"
	"gopkg.in/src-d/go-billy.v3/osfs"
	"gopkg.in/src-d/go-billy.v3/util"
	"gopkg.in/src-d/go-siva.v1"
)

const (
	defaultBase = "/"
)

var (
	ErrNonSeekableFile          = errors.New("file non-seekable")
	ErrAlreadyClosed            = errors.New("file was already closed")
	ErrFileWriteModeAlreadyOpen = errors.New("previous file in write mode already open")
	ErrReadOnlyFile             = errors.New("file is read-only")
	ErrWriteOnlyFile            = errors.New("file is write-only")
)

type SivaFS interface {
	billy.Basic
	billy.Dir

	// Sync closes any open files, this method should be called at the end of
	// program to ensure that all the files are properly closed, otherwise the
	// siva file will be corrupted.
	Sync() error
}

type sivaFS struct {
	underlying billy.Filesystem
	path       string
	f          billy.File
	rw         *siva.ReadWriter

	fileWriteModeOpen bool
}

// New creates a new filesystem backed by a siva file with the given path in
// the given filesystem. The siva file will be opened or created lazily with
// the first operation.
//
// All files opened in write mode must be closed, otherwise the siva file will
// be corrupted.
func New(fs billy.Filesystem, path string) SivaFS {
	return &sivaFS{
		underlying: fs,
		path:       path,
	}
}

// Create creates a new file. This file is created using CREATE, TRUNCATE and
// WRITE ONLY flags due to limitations working on siva files.
func (fs *sivaFS) Create(path string) (billy.File, error) {
	return fs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0666))
}

func (fs *sivaFS) Open(path string) (billy.File, error) {
	return fs.OpenFile(path, os.O_RDONLY, 0)
}

func (fs *sivaFS) OpenFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	path = normalizePath(path)
	if flag&os.O_CREATE != 0 && flag&os.O_TRUNC == 0 {
		return nil, billy.ErrNotSupported
	}

	if err := fs.ensureOpen(); err != nil {
		return nil, err
	}

	if flag&os.O_CREATE != 0 {
		if fs.fileWriteModeOpen {
			return nil, ErrFileWriteModeAlreadyOpen
		}

		return fs.createFile(path, flag, mode)
	}

	return fs.openFile(path, flag, mode)
}

func (fs *sivaFS) Stat(p string) (os.FileInfo, error) {
	p = normalizePath(p)

	if err := fs.ensureOpen(); err != nil {
		return nil, err
	}

	index, err := fs.getIndex()
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

func (fs *sivaFS) ReadDir(path string) ([]os.FileInfo, error) {
	path = normalizePath(path)

	if err := fs.ensureOpen(); err != nil {
		return nil, err
	}

	index, err := fs.getIndex()
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

func (fs *sivaFS) MkdirAll(filename string, perm os.FileMode) error {
	if err := fs.ensureOpen(); err != nil {
		return err
	}

	index, err := fs.getIndex()
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
func (fs *sivaFS) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (fs *sivaFS) Remove(path string) error {
	path = normalizePath(path)

	if err := fs.ensureOpen(); err != nil {
		return err
	}

	index, err := fs.getIndex()
	if err != nil {
		return err
	}

	e := index.Find(path)

	if e != nil {
		return fs.rw.WriteHeader(&siva.Header{
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

func (fs *sivaFS) Rename(from, to string) error {
	return billy.ErrNotSupported
}

func (fs *sivaFS) Sync() error {
	return fs.ensureClosed()
}

func (fs *sivaFS) ensureOpen() error {
	if fs.rw != nil {
		return nil
	}

	f, err := fs.underlying.OpenFile(fs.path, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return err
	}

	rw, err := siva.NewReaderWriter(f)
	if err != nil {
		f.Close()
		return err
	}

	fs.rw = rw
	fs.f = f
	return nil
}

func (fs *sivaFS) ensureClosed() error {
	if fs.rw == nil {
		return nil
	}

	if err := fs.rw.Close(); err != nil {
		return err
	}

	fs.rw = nil

	f := fs.f
	fs.f = nil
	return f.Close()
}

func (fs *sivaFS) createFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	if flag&os.O_RDWR != 0 || flag&os.O_RDONLY != 0 {
		return nil, billy.ErrNotSupported
	}

	header := &siva.Header{
		Name:    path,
		Mode:    mode,
		ModTime: time.Now(),
	}

	if err := fs.rw.WriteHeader(header); err != nil {
		return nil, err
	}

	closeFunc := func() error {
		if fs.rw == nil {
			return nil
		}

		if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 {
			fs.fileWriteModeOpen = false
		}

		return fs.rw.Flush()
	}

	defer func() { fs.fileWriteModeOpen = true }()
	return newFile(path, fs.rw, closeFunc), nil
}

func (fs *sivaFS) openFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	if flag&os.O_RDWR != 0 || flag&os.O_WRONLY != 0 {
		return nil, billy.ErrNotSupported
	}

	index, err := fs.getIndex()
	if err != nil {
		return nil, err
	}

	e := index.Find(path)
	if e == nil {
		return nil, os.ErrNotExist
	}

	sr, err := fs.rw.Get(e)
	if err != nil {
		return nil, err
	}

	return openFile(path, sr), nil
}

func (fs *sivaFS) getIndex() (siva.Index, error) {
	index, err := fs.rw.Index()
	if err != nil {
		return nil, err
	}

	return index.Filter(), nil
}

func listFiles(index siva.Index, dir string) ([]os.FileInfo, error) {
	dir = addTrailingSlash(dir)

	entries, err := index.Glob(fmt.Sprintf("%s*", dir))
	if err != nil {
		return nil, err
	}

	contents := []os.FileInfo{}
	for _, e := range entries {
		contents = append(contents, newFileInfo(e))
	}

	return contents, nil
}

func getDir(index siva.Index, dir string) (os.FileInfo, error) {
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

func listDirs(index siva.Index, dir string) ([]os.FileInfo, error) {
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

	contents := []os.FileInfo{}
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

func NewFilesystem(fs billy.Filesystem, path string) billy.Filesystem {
	tempdir := "/tmp"

	temporal := osfs.New(os.TempDir())
	root := New(fs, path)

	mount := mount.New(root, tempdir, temporal)
	return &Temporal{chroot.New(mount, "/"), tempdir}
}

type Temporal struct {
	billy.Filesystem
	defaultDir string
}

func (h *Temporal) TempFile(dir, prefix string) (billy.File, error) {
	dir = h.Join(h.defaultDir, dir)

	return util.TempFile(h.Filesystem, dir, prefix)
}
