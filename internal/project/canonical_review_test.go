package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalSharedResourceChangesAreRefusedBeforeRestore(t *testing.T) {
	for _, tc := range []struct {
		name, kind, resource, use string
	}{
		{"volume", "volumes", "store", "    volumes: [store:/data]\n"},
		{"volume-long", "volumes", "store", "    volumes: [{type: volume, source: store, target: /data}]\n"},
		{"network", "networks", "store", "    networks: [store]\n"},
		{"network-map", "networks", "store", "    networks: {store: {}}\n"},
		{"implicit-default-network", "networks", "default", ""},
		{"config", "configs", "store", "    configs: [{source: store, target: /config}]\n"},
		{"secret", "secrets", "store", "    secrets: [store]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProject(t)
			content := "services:\n  one:\n    image: example/one\n" + tc.use + tc.kind + ":\n  " + tc.resource + ":\n    external: true\n    name: archived-resource\n"
			if err := p.WriteFragment("one", content); err != nil {
				t.Fatal(err)
			}
			archived, err := p.ReadFragment("one")
			if err != nil {
				t.Fatal(err)
			}
			root, _ := p.ReadCompose()
			current := strings.Replace(string(root), "services:\n", "services:\n  manual:\n    image: example/manual\n"+tc.use, 1)
			current = strings.ReplaceAll(current, "archived-resource", "current-resource")
			if err := p.WriteCompose([]byte(current)); err != nil {
				t.Fatal(err)
			}
			if err := p.ValidateServiceOwnership(map[string]string{"one": string(archived)}); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("preflight accepted a shared resource change: %v", err)
			}
			if err := p.RestoreFragment("one", string(archived), "apps", true); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("Restore accepted a shared resource change: %v", err)
			}
			root, _ = p.ReadCompose()
			if string(root) != current {
				t.Fatal("rejected resource change modified the Compose file")
			}
		})
	}
}

func TestCanonicalPrivateResourceCanChangeAndSharedEquivalentCanRestore(t *testing.T) {
	p := testProject(t)
	content := "services:\n  one:\n    image: example/one\n    volumes: [store:/data]\nvolumes:\n  store:\n    external: true\n    name: old-store\n"
	if err := p.WriteFragment("one", content); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteFragment("one", strings.ReplaceAll(content, "old-store", "new-store")); err != nil {
		t.Fatalf("private resource update was refused: %v", err)
	}
	archived, _ := p.ReadFragment("one")
	root, _ := p.ReadCompose()
	current := strings.Replace(string(root), "services:\n", "services:\n  manual:\n    image: example/manual\n    volumes: [store:/data]\n", 1)
	if err := p.WriteCompose([]byte(current)); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateServiceOwnership(map[string]string{"one": string(archived)}); err != nil {
		t.Fatalf("equivalent shared resource was refused: %v", err)
	}
	if err := p.RestoreFragment("one", string(archived), "apps", true); err != nil {
		t.Fatalf("equivalent shared resource Restore was refused: %v", err)
	}
}

func TestConsolidationRefusesSiblingEnvironmentWithoutChangingLiveFiles(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		for _, symlink := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "canonical-import", false: "legacy-migration"}[canonical], map[bool]string{true: "symlink", false: "file"}[symlink]}, "/"), func(t *testing.T) {
				p := testProject(t)
				if canonical {
					if err := p.WriteRoot(); err != nil {
						t.Fatal(err)
					}
				} else if err := p.WriteCompose([]byte("name: selfhost\ninclude: [extra/manual/compose.yml]\n")); err != nil {
					t.Fatal(err)
				}
				if err := p.MkdirAll("extra", "manual"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p.Path("extra", "manual", "compose.yml"), []byte("services:\n  manual:\n    image: ${CUSTOM_IMAGE:-example/default}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				localEnv := p.Path("extra", "manual", ".env")
				var err error
				if symlink {
					err = os.Symlink(filepath.Join(t.TempDir(), "not-present"), localEnv)
				} else {
					err = os.WriteFile(localEnv, []byte("CUSTOM_IMAGE=example/custom\n"), 0o000)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, _ := p.ReadCompose()
				if err := p.WriteRoot(); err == nil || !strings.Contains(err.Error(), "local .env") {
					t.Fatalf("local environment was silently dropped: %v", err)
				}
				after, _ := p.ReadCompose()
				if string(after) != string(before) {
					t.Fatal("rejected consolidation changed Compose")
				}
				if !canonical {
					if _, err := os.Lstat(p.Path(".env")); !os.IsNotExist(err) {
						t.Fatal("rejected consolidation wrote the Project environment")
					}
					if _, err := os.Lstat(p.Path("compose.legacy.yml")); !os.IsNotExist(err) {
						t.Fatal("rejected consolidation wrote a recovery file")
					}
				}
			})
		}
	}
}
