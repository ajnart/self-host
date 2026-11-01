package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/system"
)

func TestFreshRestoreSeparateMediaUsersChecksDestinationPlatform(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goos    string
		options string
		wantErr string
	}{
		{name: "linux-rootful", goos: "linux", options: `["name=seccomp,profile=builtin"]`},
		{name: "linux-rootless", goos: "linux", options: `["name=rootless"]`, wantErr: "explicit ownership migration"},
		{name: "linux-userns", goos: "linux", options: `["name=userns"]`, wantErr: "explicit ownership migration"},
		{name: "darwin", goos: "darwin", options: `[]`, wantErr: "require a Linux host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}})
			h.deps.GOOS = tc.goos
			h.runner.On("docker info", system.Result{Stdout: []byte(tc.options)})
			h.runner.On("getent passwd", system.Result{ExitCode: 2})
			h.runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
			file := separateMediaRestoreArchive(t)
			err := h.svc.Restore(context.Background(), file, nil, "")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				p, err := h.svc.Project()
				if err != nil || !p.Exists() {
					t.Fatalf("supported Restore did not create its Project: %v", err)
				}
				settings, err := p.LoadSettings()
				if err != nil || settings["MEDIA_SEPARATE_USERS"] != "on" || settings["SONARR_PUID"] != "10001" {
					t.Fatalf("supported Restore changed archived media identities: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("unsupported Restore error = %v, want %q", err, tc.wantErr)
			}
			for _, name := range []string{".env", "compose.yml", "state.json", "apps/sonarr", "restore-startup.json"} {
				if _, err := os.Lstat(filepath.Join(h.root(), name)); !os.IsNotExist(err) {
					t.Fatalf("unsupported Restore wrote live Project path %s", name)
				}
			}
			for _, call := range h.runner.Calls() {
				if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
					t.Fatal("unsupported Restore changed containers")
				}
			}
		})
	}
}

func TestFreshRestoreSeparateMediaUsersRefusesExistingHostIdentity(t *testing.T) {
	h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}})
	h.runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
	h.runner.On("getent passwd 10001", system.Result{Stdout: []byte("directory-user:x:10001:10000:Directory account:/home/directory-user:/bin/sh\n")})
	err := h.svc.Restore(context.Background(), separateMediaRestoreArchive(t), nil, "")
	if err == nil || !strings.Contains(err.Error(), "explicit ownership migration") {
		t.Fatalf("Restore must preserve ownership boundaries on its destination: %v", err)
	}
	for _, name := range []string{".env", "compose.yml", "state.json", "apps/sonarr", "restore-startup.json"} {
		if _, err := os.Lstat(filepath.Join(h.root(), name)); !os.IsNotExist(err) {
			t.Fatalf("refused Restore wrote live Project path %s", name)
		}
	}
	for _, call := range h.runner.Calls() {
		if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
			t.Fatal("refused Restore changed containers")
		}
	}
}

func TestFreshRestoreSeparateMediaUsersRefusesInvalidOrDuplicateOwners(t *testing.T) {
	for _, test := range []struct {
		name  string
		users map[string]string
	}{
		{"root", map[string]string{"sonarr": "0"}},
		{"negative", map[string]string{"sonarr": "-1"}},
		{"missing", map[string]string{"sonarr": ""}},
		{"nonnumeric", map[string]string{"sonarr": "abc"}},
		{"Project owner", map[string]string{"sonarr": "1000"}},
		{"duplicate App owners", map[string]string{"sonarr": "10001", "radarr": "10001"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}}, &testApp{id: "radarr", done: map[string]bool{}})
			h.runner.On("getent passwd", system.Result{ExitCode: 2})
			h.runner.On("getent passwd 0", system.Result{Stdout: []byte("root:x:0:0:root:/root:/bin/sh\n")})
			err := h.svc.Restore(context.Background(), separateMediaRestoreArchiveUsers(t, test.users), nil, "")
			if err == nil || !strings.Contains(err.Error(), "explicit ownership migration") {
				t.Fatalf("Restore accepted invalid archived media file owners: %v", err)
			}
			for _, name := range []string{".env", "compose.yml", "state.json", "apps/sonarr", "apps/radarr", "restore-startup.json"} {
				if _, err := os.Lstat(filepath.Join(h.root(), name)); !os.IsNotExist(err) {
					t.Fatalf("invalid Restore wrote live Project path %s", name)
				}
			}
			for _, call := range h.runner.Calls() {
				if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
					t.Fatal("invalid Restore changed containers")
				}
			}
		})
	}
}

func TestFreshRestoreSeparateMediaUsersRequiresUsableIdentityLookup(t *testing.T) {
	h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}})
	h.runner.On("getent passwd", system.Result{ExitCode: 127})
	err := h.svc.Restore(context.Background(), separateMediaRestoreArchive(t), nil, "")
	if err == nil || !strings.Contains(err.Error(), "getent passwd") {
		t.Fatalf("Restore accepted unavailable identity lookup: %v", err)
	}
	if p, err := h.svc.Project(); err != nil || p.Exists() {
		t.Fatalf("failed identity lookup created a Project: %v", err)
	}
}

func TestExistingProjectMediaRestorePreservesProvisionedOwners(t *testing.T) {
	h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}})
	if err := h.install("sonarr"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings["MEDIA_LAYOUT"] = "legacy"
	settings["MEDIA_SEPARATE_USERS"] = "on"
	settings["MEDIA_UMASK"] = "002"
	settings["SONARR_PUID"] = "10001"
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	h.runner.On("getent passwd", system.Result{ExitCode: 127})
	if err := h.svc.Restore(context.Background(), separateMediaRestoreArchive(t), nil, ""); err != nil {
		t.Fatalf("existing Project's provisioned media owner must remain usable: %v", err)
	}
	for _, call := range h.runner.Calls() {
		if strings.HasPrefix(call, "getent ") {
			t.Fatal("an existing Project's provisioned owner was treated as a fresh allocation")
		}
	}
}

func separateMediaRestoreArchive(t *testing.T) string {
	t.Helper()
	return separateMediaRestoreArchiveUsers(t, map[string]string{"sonarr": "10001"})
}

func separateMediaRestoreArchiveUsers(t *testing.T, users map[string]string) string {
	t.Helper()
	ids := make([]string, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var apps []backup.ManifestApp
	state := map[string]any{}
	environment := "TZ=UTC\nPUID=1000\nPGID=1000\nUMASK=022\nEXPOSURE=local\nLAN_IP=127.0.0.1\nMEDIA_LAYOUT=legacy\nMEDIA_SEPARATE_USERS=on\nMEDIA_UMASK=002\n"
	records := map[string]string{"secrets.env": "ADMIN_USER=fixture-admin\nADMIN_PASSWORD=fixture-password\n"}
	for _, id := range ids {
		apps = append(apps, backup.ManifestApp{ID: id, Managed: true})
		state[id] = map[string]string{"setup": "ok"}
		environment += strings.ToUpper(id) + "_PUID=" + users[id] + "\n"
		records["apps/"+id+"/compose.yml"] = "services:\n  " + id + ":\n    image: example/" + id + ":v1\n    volumes: [./config:/config]\n"
		records["apps/"+id+"/config/data"] = "fixture-archived-configuration"
	}
	stateJSON, err := json.Marshal(map[string]any{"version": 1, "apps": state})
	if err != nil {
		t.Fatal(err)
	}
	records["state.json"] = string(stateJSON)
	records[".env"] = environment
	file := filepath.Join(t.TempDir(), "media.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: apps})
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	for name, value := range records {
		if err := w.AddBytes(name, []byte(value), 0o600); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}
