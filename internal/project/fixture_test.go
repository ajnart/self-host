package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/system"
)

func fixtureProject(t *testing.T, source string) (*Project, FixturePolicy) {
	t.Helper()
	root := t.TempDir()
	p := &Project{Root: filepath.Join(root, "project"), ComposeName: "testkit-owned", Owner: system.User{UID: os.Getuid(), GID: os.Getgid()}}
	if err := os.Mkdir(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSettings(map[string]string{"EXPOSURE": "local", "BIND_IP": "127.0.0.1", "DATA_ROOT": filepath.Join(root, "data")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Path("compose.yml"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, FixturePolicy{Root: root, Service: "fixture"}
}

func TestFixtureComposeCapsEveryServiceIncludingDeferredDependencies(t *testing.T) {
	p, policy := fixtureProject(t, "services:\n  fixture:\n    image: example/app:latest\n    depends_on: [database]\n    ports: ['${BIND_IP}:12345:80', '${BIND_IP}:12346:81/udp']\n    volumes: ['${DATA_ROOT}:/data']\n  database:\n    image: example/db:1\n    cpus: '99'\n    mem_limit: 99g\n    pids_limit: 9999\n")
	policy.Image = "example/app:selected"
	plan, err := p.PrepareFixtureCompose(policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 2 || len(plan.Ports) != 2 || plan.Images["fixture"] != policy.Image {
		t.Fatal("incomplete fixture plan")
	}
	root, err := p.readRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range plan.Services {
		svc := field(field(root, "services"), name)
		for key, want := range map[string]string{"cpus": "1.0", "mem_limit": "1g", "pids_limit": "512"} {
			if value := field(svc, key); value == nil || value.Value != want {
				t.Fatalf("%s lacks %s", name, key)
			}
		}
	}
}

func TestFixtureComposeRefusesUnprovableScopeWithoutRewriting(t *testing.T) {
	for name, source := range map[string]string{
		"external bind":      "services:\n  fixture:\n    image: example/app:latest\n    volumes: ['/etc:/host']\n",
		"socket":             "services:\n  fixture:\n    image: example/app:latest\n    volumes: ['/var/run/docker.sock:/socket']\n",
		"host network":       "services:\n  fixture:\n    image: example/app:latest\n    network_mode: host\n",
		"fixed name":         "services:\n  fixture:\n    image: example/app:latest\n    container_name: global\n",
		"external resource":  "services:\n  fixture:\n    image: example/app:latest\nvolumes:\n  data:\n    external: true\n",
		"named volume":       "services:\n  fixture:\n    image: example/app:latest\n    volumes: ['data:/data']\nvolumes:\n  data: {}\n",
		"replicas":           "services:\n  fixture:\n    image: example/app:latest\n    deploy:\n      replicas: 9\n",
		"broad port":         "services:\n  fixture:\n    image: example/app:latest\n    ports: ['12345:80']\n",
		"unbounded image":    "services:\n  fixture:\n    image: '${IMAGE}'\n",
		"six services":       "services:\n  fixture: {image: 'example/app:latest'}\n  b: {image: 'example/db:1'}\n  c: {image: 'example/db:1'}\n  d: {image: 'example/db:1'}\n  e: {image: 'example/db:1'}\n  f: {image: 'example/db:1'}\n",
		"long external bind": "services:\n  fixture:\n    image: example/app:latest\n    volumes:\n      - {type: bind, source: /etc, target: /host}\n",
		"long socket":        "services:\n  fixture:\n    image: example/app:latest\n    volumes:\n      - {type: bind, source: '${DATA_ROOT}/docker.sock', target: /socket}\n",
		"long volume":        "services:\n  fixture:\n    image: example/app:latest\n    volumes:\n      - {type: volume, source: data, target: /data}\nvolumes:\n  data: {}\n",
		"long propagation":   "services:\n  fixture:\n    image: example/app:latest\n    volumes:\n      - {type: bind, source: '${DATA_ROOT}', target: /data, bind: {propagation: shared}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			p, policy := fixtureProject(t, source)
			if _, err := p.PrepareFixtureCompose(policy); err == nil {
				t.Fatal("unsafe Compose accepted")
			}
			data, err := os.ReadFile(p.Path("compose.yml"))
			if err != nil || string(data) != source {
				t.Fatal("refusal changed canonical Compose")
			}
		})
	}
}

func TestFixtureComposeAcceptsPrivateLongFormBinds(t *testing.T) {
	p, policy := fixtureProject(t, "services:\n  fixture:\n    image: example/app:latest\n    volumes:\n      - type: bind\n        source: ${DATA_ROOT}/library\n        target: /library\n        read_only: true\n        bind:\n          create_host_path: false\n")
	if _, err := p.PrepareFixtureCompose(policy); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureComposeRefusesOverridesAndSymlinkEscapes(t *testing.T) {
	for _, kind := range []string{"override", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			p, policy := fixtureProject(t, "services:\n  fixture:\n    image: example/app:latest\n    volumes: ['./config:/config']\n")
			if kind == "override" {
				if err := os.WriteFile(p.Path("compose.override.yml"), []byte("services: {}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(t.TempDir(), p.Path("config")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.PrepareFixtureCompose(policy); err == nil {
				t.Fatal("escape accepted")
			}
		})
	}
}

func TestFixtureImageRefusesInterpolationAndCredentials(t *testing.T) {
	for _, value := range []string{"${IMAGE}", "image\npassword", "https://example/app:latest", "user:password@example/app:latest", strings.Repeat("a", 513)} {
		if ValidFixtureImage(value) {
			t.Fatal("unsafe selected image accepted")
		}
	}
}

func TestFixtureComposePairImageOverrides(t *testing.T) {
	p, policy := fixtureProject(t, "services:\n  fixture: {image: 'example/app:latest'}\n  peer: {image: 'example/peer:latest'}\n")
	policy.Images = map[string]string{"fixture": "example/app:selected", "peer": "example/peer:selected"}
	plan, err := p.PrepareFixtureCompose(policy)
	if err != nil || plan.Images["fixture"] != "example/app:selected" || plan.Images["peer"] != "example/peer:selected" {
		t.Fatal("pair overrides not applied", err)
	}
	policy.Images["peer"] = "${UNSAFE}"
	if _, err := p.PrepareFixtureCompose(policy); err == nil {
		t.Fatal("unsafe pair image accepted")
	}
}
