package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/maintainerr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func maintainerrDirectoryHTTP(t *testing.T, h *harness) {
	t.Helper()
	h.deps.HTTP = &http.Client{Transport: tailscaleHTTP(func(r *http.Request) (*http.Response, error) {
		responses := map[string]string{
			"/api/health/ready":           `{"status":"ok","database":"ok"}`,
			"/api/settings/test/setup":    `true`,
			"/api/media-server/type":      `{"type":"jellyfin"}`,
			"/api/media-server":           `{"machineId":"synthetic-server","version":"1.2.3"}`,
			"/api/media-server/users":     `[{"id":"synthetic-owner"}]`,
			"/api/media-server/libraries": `[]`,
		}
		body, ok := responses[r.URL.Path]
		if !ok || r.Method != http.MethodGet {
			t.Errorf("unexpected consumer request: %s %s", r.Method, r.URL.Path)
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

func TestMaintainerrDirectoryMissingInstallRemainsHeldBeforeSharedPreparation(t *testing.T) {
	h := newDirectoryHarness(t, maintainerr.App{})
	if err := h.install("maintainerr"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("missing storage unexpectedly passed the shared early/startup Preflight: %v", err)
	}
	if e := h.rec.find("maintainerr", "directories"); e != nil {
		t.Fatal("this blocked initial Install was incorrectly represented as reaching directory preparation")
	}
	if _, err := os.Lstat(filepath.Join(h.root(), "apps", "maintainerr", "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("early preflight created or adopted missing storage")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

func TestMaintainerrDirectoryInstallPreservesPrivateDataAndOldMarker(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	h := newDirectoryHarness(t, maintainerr.App{})
	maintainerrDirectoryHTTP(t, h)
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(data, ".keep")
	if err := os.WriteFile(marker, []byte("operator-owned marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(data)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.install("maintainerr"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(data)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("Install changed existing private directory metadata")
	}
	if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "operator-owned marker" {
		t.Fatal("Install removed or rewrote an old marker")
	}
	if e := h.rec.find("maintainerr", "directories"); e == nil || e.Event != events.StepOK {
		t.Fatal("compatible Install did not reach the shared declaration action")
	}
}

func TestMaintainerrDirectoryInstallUnsupportedNativeACLModelStaysManual(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this case checks the unsupported native non-Linux ACL boundary")
	}
	h := newDirectoryHarness(t, maintainerr.App{})
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.install("maintainerr"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("fake Linux daemon information bypassed the real ACL boundary: %v", err)
	}
	if e := h.rec.find("maintainerr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "ACL") {
		t.Fatal("compatible early preflight did not reach the shared ACL admission boundary")
	}
	after, err := os.Stat(data)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("unsupported native storage was changed")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

type maintainerrDirectoryRefreshApp struct {
	maintainerr.App
	revised bool
}

func (a *maintainerrDirectoryRefreshApp) Compose(rc app.RenderContext) string {
	definition := a.App.Compose(rc)
	if a.revised {
		definition = strings.Replace(definition, "restart: unless-stopped", "restart: unless-stopped\n    labels:\n      self-host-consumer-revision: updated", 1)
	}
	return definition
}

func TestMaintainerrDirectoryGeneratedRefreshPreparesMissingDataBeforeUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	a := &maintainerrDirectoryRefreshApp{}
	h := newDirectoryHarness(t, a)
	maintainerrDirectoryHTTP(t, h)
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.install("maintainerr"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	a.revised = true
	starts := 0
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.Contains(systemtest.Line(cmd), "up -d") {
			starts++
			info, err := os.Stat(data)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("generated refresh reached Up before private storage preparation")
			}
			owner := info.Sys().(*syscall.Stat_t)
			if int(owner.Uid) != os.Geteuid() || int(owner.Gid) != os.Getegid() {
				t.Error("generated refresh prepared the wrong runtime ownership")
			}
		}
		return system.Result{}
	})
	if err := h.svc.Update(t.Context(), []string{"maintainerr"}); err != nil || starts != 1 {
		t.Fatalf("generated refresh did not prepare before startup: %v, starts=%d", err, starts)
	}
}

func TestMaintainerrDirectoryGeneratedRefreshPreservesIncompatibleData(t *testing.T) {
	for _, conflict := range []string{"public", "symlink"} {
		t.Run(conflict, func(t *testing.T) {
			h := newDirectoryHarness(t, &testApp{id: "maintainerr", done: map[string]bool{}})
			if err := h.install("maintainerr"); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(h.root(), "apps", "maintainerr", "data")
			if conflict == "public" {
				if err := os.Mkdir(data, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(data, ".keep"), []byte("preserve marker"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), data); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(data)
			if err != nil {
				t.Fatal(err)
			}
			h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}}, nil)
			calls := len(h.runner.Calls())
			if err := h.svc.Update(t.Context(), []string{"maintainerr"}); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("incompatible generated-refresh data did not remain Manual: %v", err)
			}
			after, err := os.Lstat(data)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("generated refresh changed existing storage")
			}
			if conflict == "public" {
				if bytes, err := os.ReadFile(filepath.Join(data, ".keep")); err != nil || string(bytes) != "preserve marker" {
					t.Fatal("generated refresh modified an old marker")
				}
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
		})
	}
}

func TestMaintainerrDirectoryStartRestartAndRestoreKeepStrictMissingDataHold(t *testing.T) {
	for _, operation := range []string{"start", "restart", "restore"} {
		t.Run(operation, func(t *testing.T) {
			h := newDirectoryHarness(t, &testApp{id: "maintainerr", done: map[string]bool{}})
			if err := h.install("maintainerr"); err != nil {
				t.Fatal(err)
			}
			h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}}, nil)
			var archive string
			if operation == "restore" {
				var err error
				archive, err = h.svc.Backup(t.Context(), []string{"maintainerr"}, filepath.Join(t.TempDir(), "synthetic.tar.zst"), "")
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := len(h.runner.Calls())
			var err error
			switch operation {
			case "start":
				err = h.svc.Start(context.Background(), []string{"maintainerr"})
			case "restart":
				err = h.svc.Restart(context.Background(), []string{"maintainerr"})
			case "restore":
				err = h.svc.Restore(context.Background(), archive, []string{"maintainerr"}, "")
			}
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("%s lost the missing-data guard: %v", operation, err)
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
			for _, call := range h.runner.Calls()[calls:] {
				if strings.Contains(call, " restart ") || strings.Contains(call, " stop ") || strings.Contains(call, " rm ") {
					t.Fatalf("%s changed containers before storage admission", operation)
				}
			}
		})
	}
}
