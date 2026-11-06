//go:build linux || darwin

package toolfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestContainerConsumerUsesUnixOwnershipAndKernelPermissions(t *testing.T) {
	opts := fuseOptionsFor("account-name", MountConfig{ContainerUID: 65534})
	if !opts.AllowOther || opts.UID != 65534 || opts.GID != 65534 || !slices.Contains(opts.Options, "default_permissions") {
		t.Fatalf("non-root consumer cannot access the root-mounted view with correct ownership: %+v", opts)
	}
	if *opts.EntryTimeout != 0 || *opts.AttrTimeout != 0 || *opts.NegativeTimeout != 0 {
		t.Fatal("consumer support must not extend revocation caches")
	}
	legacy := fuseOptionsFor("account-name", MountConfig{})
	if legacy.AllowOther || legacy.UID != 0 || legacy.GID != 0 || len(legacy.Options) != 0 {
		t.Fatal("zero-value library configuration must preserve root-only mounts")
	}
}

func TestContainerMountRequiresPrivateRootOwnedAncestors(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to validate the daemon's host ownership contract")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateMountRoot(base); err == nil {
		t.Fatal("public root accepted")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateMountRoot(base); err != nil {
		t.Fatal(err)
	}
	bind := filepath.Join(base, "bind")
	if err := os.Mkdir(bind, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateContainerMountLocation(bind); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bind, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := validateContainerMountLocation(bind); err == nil {
		t.Fatal("writable path accepted")
	}
	if err := os.Symlink(base, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := validateContainerMountLocation(filepath.Join(base, "link")); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestContainerBindTraversalWithPrivateServiceUmask(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires the root daemon's directory ownership contract")
	}
	if os.Getenv("TOOLFS_TEST_PRIVATE_UMASK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestContainerBindTraversalWithPrivateServiceUmask$")
		cmd.Env = append(os.Environ(), "TOOLFS_TEST_PRIVATE_UMASK=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("private-umask daemon: %v\n%s", err, output)
		}
		return
	}
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	for _, existing := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t/fail=%t", existing, fail), func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "mounts")
				s, err := NewMountSet(MountSetConfig{Root: root, MountOptions: MountConfig{ContainerUID: 65534}},
					NewStaticIdentityResolver(map[string]ZimaIdentity{"7": {UserID: 7, Username: "alice"}}),
					NewStaticPermissionProvider(nil))
				if err != nil {
					t.Fatal(err)
				}
				layout, err := mountLayoutFor(root, &ZimaIdentity{UserID: 7}, "blue")
				if err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := os.MkdirAll(layout.bind, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				rejected := errors.New("fixture mount failure")
				s.mount = func(_ context.Context, _ IdentityResolver, _ PermissionProvider, _, _ string, point string, _ AgentCapabilities, _ MountConfig) (MountHandle, error) {
					for path, want := range map[string]os.FileMode{root: 0o700, layout.user: 0o700, layout.bind: 0o755} {
						info, err := os.Stat(path)
						if err != nil || info.Mode().Perm() != want {
							t.Fatalf("mode for %s: %v, %v; want %o", path, info, err, want)
						}
					}
					if fail {
						return nil, rejected
					}
					return &cleanupMountHandle{point: point}, nil
				}
				_, err = s.Mount(t.Context(), "7", "blue", nil, nil)
				if fail {
					if !errors.Is(err, rejected) {
						t.Fatal(err)
					}
					if existing {
						info, err := os.Stat(layout.bind)
						if err != nil || info.Mode().Perm() != 0o700 {
							t.Fatalf("failed mount changed original mode: %v, %v", info, err)
						}
					} else if _, err := os.Stat(layout.bind); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("new failed bind survived: %v", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Unmount("7", "blue"); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
