//go:build linux || darwin

package toolfs

import (
	"os"
	"path/filepath"
	"slices"
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
