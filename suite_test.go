package sivafs

import (
	"io/ioutil"
	"os"

	. "gopkg.in/check.v1"
	"gopkg.in/src-d/go-billy.v2"
	"gopkg.in/src-d/go-billy.v2/osfs"
	"gopkg.in/src-d/go-billy.v2/test"
)

type FilesystemSuite struct {
	test.FilesystemSuite
	FS   billy.Filesystem
	path string
}

var _ = Suite(&FilesystemSuite{})

func (s *FilesystemSuite) SetUpTest(c *C) {
	s.path, _ = ioutil.TempDir(os.TempDir(), "go-git-fs-test")
	osFs := osfs.New(s.path)
	f, err := osFs.TempFile("", "siva-fs")
	c.Assert(err, IsNil)
	name := f.Filename()
	c.Assert(f.Close(), IsNil)
	fs := New(osFs, name)

	s.FS = fs
	s.FilesystemSuite.FS = fs
}

func (s *FilesystemSuite) TestTempFile(c *C) {
	c.Skip("TempFile not supported")
}

func (s *FilesystemSuite) TestTempFileFullWithPath(c *C) {
	c.Skip("TempFile not supported")
}

func (s *FilesystemSuite) TestTempFileWithPath(c *C) {
	c.Skip("TempFile not supported")
}

func (s *FilesystemSuite) TestRemoveTempFile(c *C) {
	c.Skip("TempFile not supported")
}

func (s *FilesystemSuite) TestRename(c *C) {
	c.Skip("Rename not supported")
}

func (s *FilesystemSuite) TestOpenFileAppend(c *C) {
	c.Skip("O_APPEND not supported")
}

func (s *FilesystemSuite) TestOpenFileNoTruncate(c *C) {
	c.Skip("O_CREATE without O_TRUNC not supported")
}

func (s *FilesystemSuite) TestOpenFileReadWrite(c *C) {
	c.Skip("O_RDWR not supported")
}

func (s *FilesystemSuite) TestSeekToEndAndWrite(c *C) {
	c.Skip("does not support seek on writeable files")
}

func (s *FilesystemSuite) TestReadAtOnReadWrite(c *C) {
	c.Skip("ReadAt not supported on writeable files")
}

func (s *FilesystemSuite) TestMkdirAll(c *C) {
	c.Skip("MkdirAll method does nothing")
}

func (s *FilesystemSuite) TestMkdirAllIdempotent(c *C) {
	c.Skip("MkdirAll method does nothing")
}

func (s *FilesystemSuite) TestMkdirAllNested(c *C) {
	c.Skip("because MkdirAll does nothing, is not possible to check the " +
		"Stat of a directory created with this mehtod")
}

func (s *FilesystemSuite) TestStatDir(c *C) {
	c.Skip("StatDir is not possible because directories do not exists in siva")
}

func (s *FilesystemSuite) TestCreateDir(c *C) {
	c.Skip("CreateDir always returns no error")
}

func (s *FilesystemSuite) TestRenameToDir(c *C) {
	c.Skip("Dir renaming not supported")
}

func (s *FilesystemSuite) TestRenameDir(c *C) {
	c.Skip("Dir renaming not supported")
}

func (s *FilesystemSuite) TestFileNonRead(c *C) {
	c.Skip("Is not possible to write a file and then read it at the same time")
}

func (s *FilesystemSuite) TestFileWrite(c *C) {
	c.Skip("Open method open a file in write only mode")
}

func (s *FilesystemSuite) TestSymlinkToDir(c *C) {
	err := billy.WriteFile(s.FS, "dir/file", nil, 0644)
	c.Assert(err, IsNil)

	err = s.FS.Symlink("dir", "link")
	c.Assert(err, IsNil)

	fi, err := s.FS.Stat("link")
	c.Assert(err, IsNil)
	c.Assert(fi.Name(), Equals, "link")
	c.Assert(fi.Mode()&os.ModeSymlink, Not(Equals), 0)
	c.Assert(fi.IsDir(), Equals, true)
}

func (s FilesystemSuite) TestSymlinkRename(c *C) {
	c.Skip("Rename not supported")
}
