package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageMountEvidenceUsesTheDeepestBoundaryAndMountFlags(t *testing.T) {
	mounts := `20 0 0:1 / / rw - rootfs rootfs rw
21 20 8:1 / /mnt/pool rw - xfs /dev/sda rw
22 21 8:2 / /mnt/pool2 ro - ext4 /dev/sdb ro
23 21 8:3 / /mnt/pool/app\040data rw,noexec - btrfs /dev/sdc ro
24 21 0:4 / /mnt/pool/self-host rw - tmpfs tmpfs rw
`
	for _, tc := range []struct {
		path, existing, mount, kind string
		readOnly, noExec            bool
	}{
		{"/mnt/pool/home/new", "/mnt/pool/home", "/mnt/pool", "xfs", false, false},
		{"/mnt/pool2/home", "/mnt/pool2", "/mnt/pool2", "ext4", true, false},
		{"/mnt/pool/app data/home", "/mnt/pool/app data", "/mnt/pool/app data", "btrfs", true, true},
		{"/mnt/pool/self-host", "/mnt/pool/self-host", "/mnt/pool/self-host", "tmpfs", false, false},
		{"/mnt/not-mounted/home", "/mnt", "/", "rootfs", false, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := storageFromMounts(tc.path, tc.existing, mounts)
			if err != nil || got.Path != tc.path || got.MountPoint != tc.mount || got.Type != tc.kind || got.ReadOnly != tc.readOnly || got.NoExec != tc.noExec {
				t.Fatalf("storage = %+v, %v", got, err)
			}
		})
	}
	if _, err := storageFromMounts("/home", "/home", "malformed mount data"); err == nil {
		t.Fatal("invented backing storage without mount evidence")
	}
}

func TestStoragePathResolvesFutureHomeAndRetainedBinarySymlinksWithoutWrites(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "pool")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	resolved, existing, err := storagePath(filepath.Join(link, "new-home", "apps"))
	if err != nil || resolved != filepath.Join(parent, "new-home", "apps") || existing != parent {
		t.Fatalf("future Home folder = %q, %q, %v", resolved, existing, err)
	}
	if _, err := os.Stat(filepath.Join(parent, "new-home")); !os.IsNotExist(err) {
		t.Fatalf("inspection created the Home folder: %v", err)
	}
	binary := filepath.Join(parent, "self-host")
	if err := os.WriteFile(binary, []byte("fixture-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved, existing, err = storagePath(filepath.Join(link, "self-host"))
	if err != nil || resolved != binary || existing != binary {
		t.Fatalf("binary = %q, %q, %v", resolved, existing, err)
	}
	if err := os.Symlink(filepath.Join(parent, "absent"), filepath.Join(parent, "broken")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := storagePath(filepath.Join(parent, "broken", "home")); err == nil {
		t.Fatal("accepted an unresolved storage symlink")
	}
}
