package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestCleanupIsOwnerScopedAndIdempotent(t *testing.T) {
	manifest := ownedManifest(t)
	firstOwner, secondOwner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	first, err := registerParent(t.Context(), manifest, firstOwner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registerParent(t.Context(), manifest, secondOwner)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(first, "fixture")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	cleanup := func(ctx context.Context, root string) error {
		calls++
		if root != child || ctx.Err() != nil {
			t.Error("cleanup escaped its ownership")
		}
		return os.Remove(root)
	}
	for range 2 {
		if err := cleanupManifestOwned(t.Context(), manifest, firstOwner, cleanup); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("cleanup was not idempotent")
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("another helper's parent was removed")
	}
	if err := cleanupManifest(t.Context(), manifest, cleanup); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsSymlinksAndPublicPermissions(t *testing.T) {
	manifest := ownedManifest(t)
	if err := os.WriteFile(manifest, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := registerParent(t.Context(), manifest, strings.Repeat("a", 32)); err == nil {
		t.Fatal("public manifest accepted")
	}
	link := filepath.Join(filepath.Dir(manifest), "linked-manifest")
	if err := os.Symlink(manifest, link); err != nil {
		t.Fatal(err)
	}
	if _, err := registerParent(t.Context(), link, strings.Repeat("a", 32)); err == nil {
		t.Fatal("symlink manifest accepted")
	}
}

func TestManifestRefusesTraversalAndUnownedChildren(t *testing.T) {
	manifest := ownedManifest(t)
	owner := strings.Repeat("a", 32)
	parent, err := registerParent(t.Context(), manifest, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(manifest), filepath.Join(parent, "escape")); err != nil {
		t.Fatal(err)
	}
	called := false
	cleanup := func(context.Context, string) error {
		called = true
		return nil
	}
	if cleanupManifest(t.Context(), manifest, cleanup) == nil || called {
		t.Fatal("unowned child reached core cleanup")
	}
	if err := os.WriteFile(manifest, []byte(`{"version":1,"parent":"../escape","token":"`+strings.Repeat("a", 64)+`","owner":"`+owner+`"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cleanupManifest(t.Context(), manifest, cleanup) == nil || called {
		t.Fatal("manifest traversal reached core cleanup")
	}
}
