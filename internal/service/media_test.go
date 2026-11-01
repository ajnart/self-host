package service

import (
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestMediaSettingsDefaultSharedWithOptionalUsersOff(t *testing.T) {
	deps := systemtest.NewDeps(t)
	svc := New(deps, events.Discard(), nil)
	settings := map[string]string{"DATA_ROOT": filepath.Join(t.TempDir(), "data"), "PUID": "1000", "PGID": "1000", "UMASK": "022"}
	if err := svc.initializeMediaSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if settings["MEDIA_LAYOUT"] != "shared" || settings["MEDIA_SEPARATE_USERS"] != "off" || settings["MEDIA_COPY_FALLBACK"] != "off" || settings["MEDIA_UMASK"] != "022" {
		t.Fatalf("defaults = %#v", settings)
	}
	if settings["MEDIA_ROOT"] != filepath.Join(settings["DATA_ROOT"], "media") || settings["DOWNLOADS_ROOT"] != filepath.Join(settings["DATA_ROOT"], "torrents") {
		t.Fatal("shared storage roots must use the same parent")
	}
	if settings["SONARR_PUID"] != "" {
		t.Fatal("off must not allocate separate file owners")
	}
}

func TestSeparateMediaUsersHaveStableDistinctIDsWithoutChangingOtherApps(t *testing.T) {
	svc := New(systemtest.NewDeps(t), events.Discard(), nil)
	settings := map[string]string{"MEDIA_LAYOUT": "shared", "DATA_ROOT": t.TempDir(), "PUID": "1000", "PGID": "1000", "UMASK": "022", "MEDIA_SEPARATE_USERS": "on"}
	if err := svc.initializeMediaSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	before := maps.Clone(settings)
	if err := svc.ensureMediaIdentities(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(before, settings) || settings["PUID"] != "1000" || settings["UMASK"] != "022" || settings["MEDIA_UMASK"] != "002" {
		t.Fatalf("identity selection changed persisted or global settings: %#v", settings)
	}
	seen := map[string]bool{"1000": true, "0": true}
	for _, id := range mediaIdentityApps {
		value := settings[mediaUIDKey(id)]
		if value == "" || seen[value] {
			t.Fatalf("duplicate or absent owner for %s: %q", id, value)
		}
		seen[value] = true
	}
}

func TestSeparateMediaUsersRefuseMappedDockerIdentities(t *testing.T) {
	for _, option := range []string{"name=rootless", "name=userns"} {
		t.Run(option, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			deps.Runner.(*systemtest.Runner).On("docker info", system.Result{Stdout: []byte(`["` + option + `"]`)})
			svc := New(deps, events.Discard(), nil)
			settings := map[string]string{"PUID": "1000", "MEDIA_SEPARATE_USERS": "on"}
			if err := svc.ensureMediaIdentities(context.Background(), settings); err == nil || !strings.Contains(err.Error(), "explicit ownership migration") {
				t.Fatalf("mapped identity must require an explicit migration: %v", err)
			}
			if settings["SONARR_PUID"] != "" {
				t.Fatal("failed validation must not allocate users")
			}
		})
	}
}

func TestExistingMediaPathsAndOwnersRequireExplicitMigration(t *testing.T) {
	svc := New(systemtest.NewDeps(t), events.Discard(), catalog.New([]app.App{arr.Sonarr()}, nil))
	previous := map[string]string{"PUID": "1000", "PGID": "1000", "UMASK": "022", "MEDIA_ROOT": "/old/media", "DOWNLOADS_ROOT": "/old/downloads"}
	state := &project.State{Apps: map[string]*project.AppState{"sonarr": {}}}
	for _, change := range []map[string]string{
		{"MEDIA_LAYOUT": "shared", "DATA_ROOT": "/new/data"},
		{"MEDIA_SEPARATE_USERS": "on"},
		{"MEDIA_ROOT": "/new/media"},
	} {
		next := maps.Clone(previous)
		maps.Copy(next, change)
		if err := svc.validateMediaChange(context.Background(), previous, next, state); err == nil || !strings.Contains(err.Error(), "explicit migration") {
			t.Fatalf("change %#v should require migration: %v", change, err)
		}
	}
	if previous["MEDIA_LAYOUT"] != "" || previous["MEDIA_ROOT"] != "/old/media" {
		t.Fatal("preflight changed legacy settings")
	}
	next := maps.Clone(previous)
	next["MEDIA_COPY_FALLBACK"] = "on"
	if err := svc.validateMediaChange(context.Background(), previous, next, state); err != nil {
		t.Fatalf("copy fallback acknowledgment must remain available: %v", err)
	}
}

func TestMediaHostOwnerUsesConfiguredRootfulIDs(t *testing.T) {
	svc := New(systemtest.NewDeps(t), events.Discard(), nil)
	p := project.Open(system.User{UID: 1000, GID: 1000})
	uid, gid, err := svc.mediaHostOwner(context.Background(), p, map[string]string{"PUID": "2201", "PGID": "2202"})
	if err != nil || uid != 2201 || gid != 2202 {
		t.Fatalf("configured rootful ownership was ignored: %d:%d, %v", uid, gid, err)
	}
}

func TestMediaHostOwnerMapsRootlessContainerZeroToProjectOwner(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.Runner.(*systemtest.Runner).On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
	svc := New(deps, events.Discard(), nil)
	p := project.Open(system.User{UID: 1201, GID: 1202})
	uid, gid, err := svc.mediaHostOwner(context.Background(), p, map[string]string{"PUID": "0", "PGID": "0"})
	if err != nil || uid != 1201 || gid != 1202 {
		t.Fatalf("rootless container ownership was used directly on the host: %d:%d, %v", uid, gid, err)
	}
	if _, _, err := svc.mediaHostOwner(context.Background(), p, map[string]string{"PUID": "1000", "PGID": "1000"}); err == nil {
		t.Fatal("unknown rootless subordinate IDs were guessed")
	}
}

func TestMediaHostOwnerRequiresExplicitUserNamespaceMappings(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.Runner.(*systemtest.Runner).On("docker info", system.Result{Stdout: []byte(`["name=userns"]`)})
	svc := New(deps, events.Discard(), nil)
	p := project.Open(system.User{UID: 1000, GID: 1000})
	if _, _, err := svc.mediaHostOwner(context.Background(), p, map[string]string{"PUID": "2201", "PGID": "2202"}); err == nil || !strings.Contains(err.Error(), "explicit host mappings") {
		t.Fatal("unknown remapped ownership was accepted")
	}
}
