// Package safefs implements the filesystem invariants of the security model:
// privileged path chains are verified component by component, files are read
// without following symbolic links, writes go through a same-directory
// temporary file with explicit ownership and mode before an atomic rename, and
// a root-owned lock serialises reconciliation.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Checker verifies and creates privileged files.
//
// Root is the first path component that is verified. In production it is "/",
// so every ancestor of a privileged path is checked. Tests point it at a
// temporary directory whose own ancestors are not under their control.
type Checker struct {
	UID  int
	GID  int
	Root string
}

// Production returns the checker for the real filesystem: everything must be
// owned by root:wheel and the whole chain from "/" is verified.
func Production() Checker {
	return Checker{UID: 0, GID: 0, Root: "/"}
}

// ErrLocked is returned by Lock when another process holds the lock.
var ErrLocked = errors.New("safefs: lock is held by another process")

// MaxFileSize bounds every privileged file read.
const MaxFileSize = 1 << 20

// unsafeWriteBits are the permission bits that must never be set on a
// privileged path component.
const unsafeWriteBits = 0o022

// VerifyChain checks every component from Root down to and including path:
// the component exists, is not a symbolic link, is owned by UID, and is not
// group-writable or world-writable.
func (c Checker) VerifyChain(path string) error {
	components, err := c.components(path)
	if err != nil {
		return err
	}
	for _, p := range components {
		if _, err := c.lstatChecked(p); err != nil {
			return err
		}
	}
	return nil
}

// VerifyDir verifies the chain and requires the final component to be a
// directory with exactly mode (when mode is non-zero).
func (c Checker) VerifyDir(path string, mode fs.FileMode) error {
	if err := c.VerifyChain(path); err != nil {
		return err
	}
	fi, err := c.lstatChecked(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("safefs: %s: not a directory", path)
	}
	return checkMode(path, fi, mode)
}

// VerifyFile verifies the chain and requires the final component to be a
// regular file with exactly mode (when mode is non-zero).
func (c Checker) VerifyFile(path string, mode fs.FileMode) error {
	if err := c.VerifyChain(path); err != nil {
		return err
	}
	fi, err := c.lstatChecked(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("safefs: %s: not a regular file", path)
	}
	return checkMode(path, fi, mode)
}

// VerifyParent verifies the chain up to the directory containing path. It is
// used before creating path.
func (c Checker) VerifyParent(path string) error {
	return c.VerifyDir(filepath.Dir(path), 0)
}

// Exists reports whether path exists without following a final symbolic
// link.
func Exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// EnsureDir creates path with mode when it does not exist and then verifies
// it. The parent chain must already be safe; the installer never repairs an
// unsafe ancestor.
func (c Checker) EnsureDir(path string, mode fs.FileMode) error {
	if err := c.VerifyParent(path); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return fmt.Errorf("safefs: %s: exists and is not a directory", path)
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		if err := os.Chown(path, c.UID, c.GID); err != nil {
			return fmt.Errorf("safefs: chown %s: %w", path, err)
		}
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("safefs: chmod %s: %w", path, err)
		}
	default:
		return err
	}
	return c.VerifyDir(path, mode)
}

// WriteFile atomically replaces path with data. The parent chain is verified,
// an existing symbolic link at path is rejected, data is written to a
// same-directory temporary file that is chowned and chmodded before the
// rename, and the result is verified afterwards.
func (c Checker) WriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := c.VerifyDir(dir, 0); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("safefs: %s: refusing to replace a symbolic link", path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("safefs: %s: exists and is not a regular file", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if err := tmp.Chown(c.UID, c.GID); err != nil {
		cleanup()
		return fmt.Errorf("safefs: chown %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("safefs: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return c.VerifyFile(path, mode)
}

// ReadFile reads a privileged regular file without following a symbolic link
// at path. The parent chain is verified, the open descriptor is checked for
// type, ownership, and permissions, and the size is bounded by maxSize.
func (c Checker) ReadFile(path string, maxSize int64) ([]byte, error) {
	if err := c.VerifyDir(filepath.Dir(path), 0); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := c.checkInfo(path, fi); err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("safefs: %s: not a regular file", path)
	}
	if fi.Size() > maxSize {
		return nil, fmt.Errorf("safefs: %s: file is larger than %d bytes", path, maxSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("safefs: %s: file is larger than %d bytes", path, maxSize)
	}
	return data, nil
}

// Lock acquires an exclusive, non-blocking advisory lock on path, creating
// the lock file with mode when needed. ErrLocked is returned when another
// process holds it. The returned function releases the lock.
func (c Checker) Lock(path string, mode fs.FileMode) (release func(), err error) {
	if err := c.VerifyDir(filepath.Dir(path), 0); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("safefs: %s: lock path is not a regular file", path)
	}
	if err := c.checkInfo(path, fi); err != nil {
		f.Close()
		return nil, err
	}
	if fi.Mode().Perm() != mode {
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("safefs: flock %s: %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// RemoveFile removes path when it is a regular file or a symbolic link. It
// never follows a symbolic link and ignores a missing file.
func RemoveFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("safefs: %s: is a directory", path)
	}
	return os.Remove(path)
}

// RemoveEmptyDir removes path when it is an empty directory and ignores a
// missing directory. A non-empty directory is left in place.
func RemoveEmptyDir(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("safefs: %s: not a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("safefs: %s: directory is not empty", path)
	}
	return os.Remove(path)
}

func (c Checker) components(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("safefs: %s: privileged paths must be absolute", path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return nil, fmt.Errorf("safefs: %s: privileged paths must be clean", path)
	}
	root := filepath.Clean(c.Root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("safefs: checker root %q must be absolute", c.Root)
	}
	if clean != root && !strings.HasPrefix(clean, strings.TrimSuffix(root, "/")+"/") {
		return nil, fmt.Errorf("safefs: %s: outside privileged root %s", path, root)
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(clean, strings.TrimSuffix(root, "/")), "/")
	out := []string{root}
	if rel == "" {
		return out, nil
	}
	cur := root
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		out = append(out, cur)
	}
	return out, nil
}

func (c Checker) lstatChecked(path string) (fs.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("safefs: %s: symbolic links are not allowed in privileged paths", path)
	}
	if err := c.checkInfo(path, fi); err != nil {
		return nil, err
	}
	return fi, nil
}

func (c Checker) checkInfo(path string, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("safefs: %s: cannot determine ownership", path)
	}
	if int(st.Uid) != c.UID {
		return fmt.Errorf("safefs: %s: owned by uid %d, expected uid %d", path, st.Uid, c.UID)
	}
	if fi.Mode().Perm()&unsafeWriteBits != 0 {
		return fmt.Errorf("safefs: %s: mode %04o is group-writable or world-writable", path, fi.Mode().Perm())
	}
	return nil
}

func checkMode(path string, fi fs.FileInfo, mode fs.FileMode) error {
	if mode == 0 {
		return nil
	}
	if fi.Mode().Perm() != mode {
		return fmt.Errorf("safefs: %s: mode %04o, expected %04o", path, fi.Mode().Perm(), mode)
	}
	return nil
}
