package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalComposePreservesManualServicesAndComments(t *testing.T) {
	p := testProject(t)
	if err := p.WriteFragment("one", "services:\n  one:\n    image: example/one\n    restart: unless-stopped\n    volumes: [./config:/config]\n"); err != nil {
		t.Fatal(err)
	}
	root, err := p.ReadCompose()
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(root), "image: example/one", "# keep this image\n    image: example/custom", 1)
	edited = strings.Replace(edited, "services:\n", "services:\n  # my manual App\n  custom:\n    image: example/manual\n", 1)
	if err := p.WriteCompose([]byte(edited)); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteFragment("two", "services:\n  two:\n    image: example/two\n"); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteRoot(); err != nil {
		t.Fatal(err)
	}
	root, _ = p.ReadCompose()
	for _, want := range []string{"# keep this image", "example/custom", "# my manual App", "example/manual", "./apps/one/config:/config", "driver: local"} {
		if !strings.Contains(string(root), want) {
			t.Errorf("canonical Compose lost %q", want)
		}
	}
	if !p.IsExtra("custom") {
		t.Fatal("manual root service not discovered")
	}
	services, err := p.FragmentServices("custom")
	if err != nil || len(services) != 1 || services[0] != "custom" {
		t.Fatal(services, err)
	}
	if err := p.RemoveFragment("two"); err != nil {
		t.Fatal(err)
	}
	root, _ = p.ReadCompose()
	if strings.Contains(string(root), "example/two") || !strings.Contains(string(root), "example/custom") {
		t.Fatal("remove changed unrelated App")
	}
}

func TestLegacyComposeMigrationRebasesResourcesAndPreservesProjectIdentity(t *testing.T) {
	p := testProject(t)
	p.ComposeName = "existing-project"
	if err := p.MkdirAll("apps", "one"); err != nil {
		t.Fatal(err)
	}
	fragment := "services:\n  one:\n    image: example/one\n    volumes: [./config:/config, database:/database]\n    devices: [vendor.example/gpu=all, ./render:/dev/render, /dev/dri:/dev/dri]\n    env_file: [./custom.env]\n    build:\n      context: ./source\n      dockerfile: Containerfile\n    configs: [app-config]\nconfigs:\n  app-config:\n    file: ./settings.conf\nvolumes:\n  database: {}\n"
	if err := os.WriteFile(p.FragmentPath("one"), []byte(fragment), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path("compose.yml"), []byte("name: selfhost\ninclude: [apps/one/compose.yml]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSettings(map[string]string{"TZ": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path("secrets.env"), []byte("ADMIN_PASSWORD=synthetic-private-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteRoot(); err != nil {
		t.Fatal(err)
	}
	root, _ := p.ReadCompose()
	for _, want := range []string{"name: existing-project", "./apps/one/config:/config", "database:/database", "./apps/one/custom.env", "context: ./apps/one/source", "dockerfile: Containerfile", "file: ./apps/one/settings.conf", "vendor.example/gpu=all", "./apps/one/render:/dev/render", "/dev/dri:/dev/dri"} {
		if !strings.Contains(string(root), want) {
			t.Errorf("migration missing %q", want)
		}
	}
	if strings.Contains(string(root), "./apps/one/vendor.example/gpu=all") {
		t.Fatal("migration rebased a CDI selector as a host path")
	}
	if strings.Contains(string(root), "include:") {
		t.Fatal("canonical file still includes fragments")
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := settings["ADMIN_PASSWORD"]; ok {
		t.Fatal("credential exposed as Host setting")
	}
	secrets, err := p.LoadSecrets()
	if err != nil || secrets["ADMIN_PASSWORD"] != "synthetic-private-value" {
		t.Fatal("credential migration failed")
	}
	info, _ := os.Stat(p.Path(".env"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("Compose environment is not private")
	}
	t.Run("legacy interpolation", func(t *testing.T) {
		p := testProject(t)
		if err := p.WriteCompose([]byte("include: []\n")); err != nil {
			t.Fatal(err)
		}
		legacy := "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\nLITERAL='${Z_BASE}'\n"
		if err := p.WriteHomeFile("secrets.env", []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		secrets, err := p.LoadSecrets()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SaveSecrets(secrets); err != nil {
			t.Fatal(err)
		}
		if err := p.WriteRoot(); err != nil {
			t.Fatal(err)
		}
		data, err := p.ReadHomeFile(".env")
		if err != nil || !strings.Contains(string(data), legacy) {
			t.Fatal("consolidation changed legacy assignment order or interpolation", err)
		}
		secrets["Z_BASE"] = "fixture-updated"
		delete(secrets, "LITERAL")
		if err := p.SaveSecrets(secrets); err != nil {
			t.Fatal(err)
		}
		data, err = p.ReadHomeFile("secrets.env")
		if err != nil || !strings.Contains(string(data), "Z_BASE=fixture-updated\nA_TOKEN=${Z_BASE}/token\n") || strings.Contains(string(data), "LITERAL=") {
			t.Fatal("legacy synchronization changed interpolation or resurrected a retired assignment", err)
		}
	})
}

func TestUnsafeLegacyMigrationLeavesLiveFilesUnchanged(t *testing.T) {
	p := testProject(t)
	if err := p.MkdirAll("apps", "one"); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("include: [apps/one/compose.yml]\n")
	fragment := "services:\n  one:\n    extends:\n      file: ../base.yml\n      service: base\n"
	os.WriteFile(p.Path("compose.yml"), legacy, 0o644)
	os.WriteFile(p.FragmentPath("one"), []byte(fragment), 0o644)
	if err := p.WriteRoot(); err == nil {
		t.Fatal("unsafe extends migration accepted")
	}
	root, _ := p.ReadCompose()
	if string(root) != string(legacy) {
		t.Fatal("failed migration changed original Compose")
	}
	if _, err := os.Stat(p.Path(".env")); !os.IsNotExist(err) {
		t.Fatal("failed validation mutated environment")
	}
}

func TestPrivateEnvironmentMergesSettingsSecretsAndManualValues(t *testing.T) {
	p := testProject(t)
	if err := p.WriteHomeFile(".env", []byte("# manual interpolation\nCUSTOM='value with spaces'\nTZ=UTC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSettings(map[string]string{"TZ": "Europe/Paris", "ADMIN_PASSWORD": "must-not-be-a-setting"}); err != nil {
		t.Fatal(err)
	}
	secrets, err := p.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	secrets["ADMIN_PASSWORD"] = "synthetic-$value"
	if err := p.SaveSecrets(secrets); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSettings(map[string]string{"TZ": "UTC"}); err != nil {
		t.Fatal(err)
	}
	settings, _ := p.LoadSettings()
	if len(settings) != 1 || settings["TZ"] != "UTC" {
		t.Fatal("public setting filter failed")
	}
	secrets, _ = p.LoadSecrets()
	if secrets["CUSTOM"] != "value with spaces" || secrets["ADMIN_PASSWORD"] != "synthetic-$value" {
		t.Fatal("private values lost")
	}
	data, _ := os.ReadFile(p.Path(".env"))
	if !strings.Contains(string(data), "# manual interpolation") || !strings.Contains(string(data), "'synthetic-$value'") {
		t.Fatal("env comments or literal interpolation lost")
	}
}

func TestCanonicalFragmentNormalizationAndRestoreAreStable(t *testing.T) {
	p := testProject(t)
	fragment := "services:\n  one:\n    image: example/one\n    restart: unless-stopped\n    volumes: [./config:/config]\n"
	normalized, err := p.NormalizeFragment("one", fragment)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WriteFragment("one", fragment); err != nil {
		t.Fatal(err)
	}
	got, err := p.ReadFragment("one")
	if err != nil || string(got) != string(normalized) {
		t.Fatal("rendered App snapshot is unstable", err)
	}
	if err := p.WriteCanonicalFragment("one", string(got)); err != nil {
		t.Fatal(err)
	}
	restored, _ := p.ReadFragment("one")
	if string(restored) != string(got) {
		t.Fatal("restore rebased App data a second time")
	}
}

func TestManualRootAppSnapshotRestoresReferencedResources(t *testing.T) {
	p := testProject(t)
	compose := "name: selfhost\nservices:\n  manual:\n    image: example/manual\n    volumes: [store:/data]\n    networks: [private]\n    configs: [custom]\n    secrets: [password]\nvolumes:\n  store: {external: true}\nnetworks:\n  private: {external: true}\nconfigs:\n  custom: {file: ./custom.conf}\nsecrets:\n  password: {file: ./password.txt}\n"
	if err := p.WriteCompose([]byte(compose)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.ReadFragment("manual")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"store:", "private:", "custom:", "password:"} {
		if !strings.Contains(string(snapshot), want) {
			t.Errorf("snapshot omitted resource %q", want)
		}
	}
	if err := p.RestoreFragment("manual", string(snapshot), ExtraDirName, true); err != nil {
		t.Fatal(err)
	}
	if !p.IsExtra("manual") {
		t.Fatal("manual service ownership changed to Catalog App")
	}
}

func TestNamesComposeManualEditsSurviveReconciliation(t *testing.T) {
	p := testProject(t)
	content := "services:\n  access-caddy:\n    image: caddy:latest\n    restart: unless-stopped\n"
	if err := p.WriteNamesFragment(content); err != nil {
		t.Fatal(err)
	}
	root, _ := p.ReadCompose()
	edited := strings.Replace(string(root), "image: caddy:latest", "# private image choice\n    image: caddy:custom", 1)
	if err := p.WriteCompose([]byte(edited)); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteNamesFragment(content); err != nil {
		t.Fatal(err)
	}
	got, _ := p.ReadCompose()
	if string(got) != edited {
		t.Fatal("Easy names reconciliation replaced a manual Compose edit")
	}
}

func TestStoredComposeNamePreservesCanonicalIdentityAndIgnoresLegacyDefault(t *testing.T) {
	p := testProject(t)
	if err := p.WriteCompose([]byte("name: selfhost\ninclude: []\n")); err != nil {
		t.Fatal(err)
	}
	if name, err := p.StoredComposeName(); err != nil || name != "" {
		t.Fatal("legacy default replaced the effective CLI identity")
	}
	if err := p.WriteCompose([]byte("name: existing-project\nservices: {}\n")); err != nil {
		t.Fatal(err)
	}
	if name, err := p.StoredComposeName(); err != nil || name != "existing-project" {
		t.Fatal("canonical identity was lost")
	}
	if err := p.WriteHomeFile(".env", []byte("COMPOSE_PROJECT_NAME=env-project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if name, err := p.StoredComposeName(); err != nil || name != "env-project" {
		t.Fatal("Compose environment precedence changed")
	}
}

func TestMalformedComposeOwnershipCannotEscapeProject(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	outside := filepath.Join(parent, "outside-data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep outside data"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Project{Root: root, ComposeName: Name}
	cases := []string{
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    ../outside-data: {folder: apps, services: [safe]}\n",
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    '..\\outside-data': {folder: extra, services: [safe]}\n",
		"services: {'../outside-data': {image: example/safe}}\n",
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    safe: {folder: ../../outside-data, services: [safe]}\n",
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    safe: {folder: access, services: [safe]}\n",
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    safe: {folder: apps, services: [safe], resources: {services: [safe]}}\n",
		"services: {safe: {image: example/safe}}\nx-self-host:\n  apps:\n    safe: {folder: apps, services: [safe], resources: {volumes: [{invalid: value}]}}\n",
	}
	for i, compose := range cases {
		if err := os.WriteFile(p.Path("compose.yml"), []byte(compose), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := p.AppIDs(); err == nil {
			t.Fatalf("malformed ownership %d was discoverable", i)
		}
		if err := p.RemoveFragment("safe"); err == nil {
			t.Fatalf("malformed ownership %d was mutable", i)
		}
		after, _ := p.ReadCompose()
		if string(after) != compose {
			t.Fatalf("malformed ownership %d mutated live Compose", i)
		}
		data, err := os.ReadFile(outside)
		if err != nil || string(data) != "keep outside data" {
			t.Fatal("malformed ownership touched outside data")
		}
	}
	if err := p.RemoveFragment("../../outside-data"); err == nil {
		t.Fatal("path traversal App ID was accepted")
	}
	if err := p.WriteFragment("../../outside-data", "services: {safe: {image: example/safe}}\n"); err == nil {
		t.Fatal("path traversal App write was accepted")
	}
}
