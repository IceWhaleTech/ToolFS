//go:build linux || darwin

package toolfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func secureTestRoot(t *testing.T) (RootHandle, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := newSecureBackend().OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, dir
}

func TestSecurePathNoFollow(t *testing.T) {
	operations := map[string]func(RootHandle, string) error{
		"ReadFile": func(h RootHandle, p string) error { _, e := h.ReadFile(p); return e },
		"OpenRead": func(h RootHandle, p string) error {
			f, e := h.OpenRead(p)
			if e == nil {
				_ = f.Close()
			}
			return e
		},
		"WriteFile": func(h RootHandle, p string) error { return h.WriteFile(p, []byte("bad"), 0600) },
		"OpenWrite": func(h RootHandle, p string) error {
			f, e := h.OpenWrite(p, 0600)
			if e == nil {
				_ = f.Close()
			}
			return e
		},
		"Create":            func(h RootHandle, p string) error { return h.Create(p, 0600) },
		"ReadDir":           func(h RootHandle, p string) error { _, e := h.ReadDir(p); return e },
		"Mkdir":             func(h RootHandle, p string) error { return h.Mkdir(p, 0700) },
		"MkdirAll":          func(h RootHandle, p string) error { return h.MkdirAll(p, 0700) },
		"Stat":              func(h RootHandle, p string) error { _, e := h.Stat(p); return e },
		"Remove":            func(h RootHandle, p string) error { return h.Remove(p) },
		"RenameSource":      func(h RootHandle, p string) error { return h.Rename(p, "moved") },
		"RenameDestination": func(h RootHandle, p string) error { return h.Rename("source", p) },
		"CloneSource":       func(h RootHandle, p string) error { return h.CloneFile(p, "clone", 0600) },
		"CloneDestination":  func(h RootHandle, p string) error { return h.CloneFile("source", p, 0600) },
	}
	for name, op := range operations {
		t.Run(name+"Parent", func(t *testing.T) {
			h, dir := secureTestRoot(t)
			if err := os.Mkdir(filepath.Join(dir, "denied"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "denied", "file"), []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("denied", filepath.Join(dir, "alias")); err != nil {
				t.Fatal(err)
			}
			p := "alias/file"
			if name == "Mkdir" || name == "MkdirAll" || name == "Create" || name == "CloneDestination" {
				p = "alias/new"
			}
			if err := op(h, p); err == nil {
				t.Fatal("followed symlink parent")
			}
			got, err := os.ReadFile(filepath.Join(dir, "denied", "file"))
			if err != nil || string(got) != "secret" {
				t.Fatalf("target changed: %q %v", got, err)
			}
		})
	}
	for _, name := range []string{"ReadFile", "OpenRead", "WriteFile", "OpenWrite", "Create", "ReadDir", "MkdirAll", "CloneSource", "CloneDestination"} {
		t.Run(name+"Leaf", func(t *testing.T) {
			h, dir := secureTestRoot(t)
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte("secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("source", filepath.Join(dir, "alias")); err != nil {
				t.Fatal(err)
			}
			if err := operations[name](h, "alias"); err == nil {
				t.Fatal("followed symlink leaf")
			}
			got, err := os.ReadFile(filepath.Join(dir, "source"))
			if err != nil || string(got) != "secret" {
				t.Fatalf("target changed: %q %v", got, err)
			}
		})
	}
}

func TestSecureRootRejectsRawParentTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dir + "/absent/../", dir + "/../" + filepath.Base(dir), dir + "\x00"} {
		if h, err := newSecureBackend().OpenRoot(name); err == nil {
			_ = h.Close()
			t.Fatalf("accepted raw traversal %q", name)
		}
	}
}

func TestSecureCloneFallbackUsesPinnedDescriptors(t *testing.T) {
	h, dir := secureTestRoot(t)
	if err := h.WriteFile("source", []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(filepath.Join(dir, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	for _, failure := range []error{ErrCloneUnsupported, syscall.EIO} {
		t.Run(failure.Error(), func(t *testing.T) {
			dst, err := os.OpenFile(filepath.Join(dir, "destination"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer dst.Close()
			moved := filepath.Join(dir, "moved")
			err = cloneOrCopy(dst, src, func(*os.File, *os.File) error {
				if err := os.Rename(filepath.Join(dir, "destination"), moved); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "destination"), []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
				return failure
			})
			if errors.Is(failure, ErrCloneUnsupported) && err != nil {
				t.Fatal(err)
			}
			if errors.Is(failure, syscall.EIO) && !errors.Is(err, syscall.EIO) {
				t.Fatalf("real I/O error hidden: %v", err)
			}
			data, readErr := os.ReadFile(filepath.Join(dir, "destination"))
			if readErr != nil || string(data) != "replacement" {
				t.Fatalf("replacement changed: %q %v", data, readErr)
			}
			if errors.Is(failure, ErrCloneUnsupported) {
				data, readErr = os.ReadFile(moved)
				if readErr != nil || string(data) != "original" {
					t.Fatalf("copy did not use original fd: %q %v", data, readErr)
				}
			}
			if err := os.Remove(filepath.Join(dir, "destination")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(moved); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSecureRootPinnedAndDirectoryMetadataSnapshot(t *testing.T) {
	h, dir := secureTestRoot(t)
	if err := h.WriteFile("file", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := h.ReadDir("")
	if err != nil || len(items) != 1 {
		t.Fatalf("readdir: %v %v", items, err)
	}
	oldInfo, err := items[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := h.ReadFile("file")
	if err != nil || string(data) != "old" {
		t.Fatalf("root reopened by name: %q %v", data, err)
	}
	heldInfo, err := items[0].Info()
	if err != nil || !os.SameFile(oldInfo, heldInfo) {
		t.Fatal("DirEntry.Info reopened replaced path")
	}
	if err := h.CloneFile("file", "copy", 0600); err != nil {
		t.Fatal(err)
	}
	data, err = h.ReadFile("copy")
	if err != nil || string(data) != "old" {
		t.Fatalf("clone/copy: %q %v", data, err)
	}
}

func TestSecurePathConcurrentReplacementAndClose(t *testing.T) {
	h, dir := secureTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "allowed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "denied"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "allowed/file"), []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "denied/file"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 200; i++ {
			if err := os.Rename(filepath.Join(dir, "allowed"), filepath.Join(dir, "held")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Symlink("denied", filepath.Join(dir, "allowed")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Remove(filepath.Join(dir, "allowed")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(filepath.Join(dir, "held"), filepath.Join(dir, "allowed")); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				data, err := h.ReadFile("allowed/file")
				if err == nil && string(data) != "public" && len(data) != 0 {
					t.Errorf("followed swapped parent: %q", data)
					return
				}
				_ = h.WriteFile("allowed/file", []byte("public"), 0600)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(dir, "denied/file"))
	if err != nil || string(data) != "secret" {
		t.Fatalf("symlink target modified: %q %v", data, err)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = h.ReadFile("allowed/file")
				_, _ = h.ReadDir("")
			}
		}()
	}
	_ = h.Close()
	wg.Wait()
	if _, err := h.ReadFile("allowed/file"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed root: %v", err)
	}
}
