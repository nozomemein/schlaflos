package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testChecker(t *testing.T) (Checker, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root}, root
}

func TestWriteFileAtomicAndVerified(t *testing.T) {
	c, root := testChecker(t)
	path := filepath.Join(root, "state.json")
	if err := c.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	data, err := c.ReadFile(path, MaxFileSize)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != `{"a":1}` {
		t.Fatalf("data = %q", data)
	}
	// Overwrite keeps the mode and replaces the content.
	if err := c.WriteFile(path, []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ = c.ReadFile(path, MaxFileSize)
	if string(data) != `{"a":2}` {
		t.Fatalf("data after overwrite = %q", data)
	}
}

func TestSymlinkRejected(t *testing.T) {
	c, root := testChecker(t)
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFile(link, []byte("y"), 0o600); err == nil {
		t.Fatal("WriteFile through symlink succeeded")
	}
	if _, err := c.ReadFile(link, MaxFileSize); err == nil {
		t.Fatal("ReadFile through symlink succeeded")
	}
	if err := c.VerifyFile(link, 0o600); err == nil {
		t.Fatal("VerifyFile on symlink succeeded")
	}
	// A symlinked directory in the chain is rejected too.
	realDir := filepath.Join(root, "real")
	os.Mkdir(realDir, 0o755)
	os.Symlink(realDir, filepath.Join(root, "dirlink"))
	os.WriteFile(filepath.Join(realDir, "f"), []byte("x"), 0o600)
	if err := c.VerifyFile(filepath.Join(root, "dirlink", "f"), 0o600); err == nil {
		t.Fatal("chain through symlinked directory accepted")
	}
	if got := string(mustRead(t, target)); got != "x" {
		t.Fatalf("target modified: %q", got)
	}
}

func TestWorldWritableAncestorRejected(t *testing.T) {
	c, root := testChecker(t)
	dir := filepath.Join(root, "loose")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o777)
	path := filepath.Join(dir, "config.toml")
	if err := c.WriteFile(path, []byte("x"), 0o600); err == nil {
		t.Fatal("write under world-writable directory succeeded")
	}
	os.Chmod(dir, 0o775)
	if err := c.VerifyDir(dir, 0); err == nil {
		t.Fatal("group-writable directory accepted")
	}
	os.Chmod(dir, 0o755)
	if err := c.VerifyDir(dir, 0o755); err != nil {
		t.Fatalf("safe directory rejected: %v", err)
	}
}

func TestWrongModeRejected(t *testing.T) {
	c, root := testChecker(t)
	path := filepath.Join(root, "f")
	os.WriteFile(path, []byte("x"), 0o644)
	if err := c.VerifyFile(path, 0o600); err == nil {
		t.Fatal("mode 0644 accepted as 0600")
	}
	if err := c.VerifyFile(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileBounded(t *testing.T) {
	c, root := testChecker(t)
	path := filepath.Join(root, "big")
	os.WriteFile(path, make([]byte, 100), 0o600)
	if _, err := c.ReadFile(path, 99); err == nil {
		t.Fatal("oversized file accepted")
	}
	if _, err := c.ReadFile(path, 100); err != nil {
		t.Fatal(err)
	}
}

func TestOutsideRootRejected(t *testing.T) {
	c, _ := testChecker(t)
	if err := c.VerifyChain("/etc/passwd"); err == nil {
		t.Fatal("path outside root accepted")
	}
	if err := c.VerifyChain("relative/path"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestEnsureDir(t *testing.T) {
	c, root := testChecker(t)
	dir := filepath.Join(root, "a")
	if err := c.EnsureDir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDir(dir, 0o755); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
	// Missing parent is not created.
	if err := c.EnsureDir(filepath.Join(root, "x", "y"), 0o755); err == nil {
		t.Fatal("EnsureDir created missing parent")
	}
}

func TestLock(t *testing.T) {
	c, root := testChecker(t)
	path := filepath.Join(root, "reconcile.lock")
	release, err := c.Lock(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lock(path, 0o600); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock err = %v, want ErrLocked", err)
	}
	release()
	release2, err := c.Lock(path, 0o600)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
	fi, _ := os.Lstat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %04o", fi.Mode().Perm())
	}
}

func TestRemoveHelpers(t *testing.T) {
	_, root := testChecker(t)
	dir := filepath.Join(root, "d")
	os.Mkdir(dir, 0o755)
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("x"), 0o600)
	if err := RemoveEmptyDir(dir); err == nil {
		t.Fatal("non-empty directory removed")
	}
	if err := RemoveFile(f); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFile(f); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := RemoveEmptyDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEmptyDir(dir); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
