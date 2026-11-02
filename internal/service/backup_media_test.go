package service

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestBackupSeparateMediaOwnersUsesScopedHelperOnRootfulDocker(t *testing.T) {
	h := newHarness(t, &testApp{id: "sonarr", done: map[string]bool{}}, &testApp{id: "other", done: map[string]bool{}})
	if err := h.install("sonarr", "other"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings["MEDIA_SEPARATE_USERS"] = "on"
	settings["SONARR_PUID"] = "10001"
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAppFile("sonarr", "config/private", []byte("fixture-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.Path("apps", "sonarr", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.Contains(systemtest.Line(cmd), " ps ") {
			return system.Result{Stdout: []byte(`{"Service":"sonarr","State":"running"}`)}
		}
		return system.Result{}
	})
	var stream bytes.Buffer
	tw := tar.NewWriter(&stream)
	if err := tw.WriteHeader(&tar.Header{Name: "./config/private", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 10001, Gid: 1000, Size: int64(len("fixture-config"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, "fixture-config"); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	h.runner.OnStreamIO("--entrypoint sh", func(_ context.Context, cmd system.Cmd, stdin io.Reader, stdout, _ io.Writer) error {
		called = true
		line := systemtest.Line(cmd)
		if !strings.Contains(line, "src="+p.Path("apps", "sonarr")+",") || strings.Contains(line, p.Path("apps", "other")) || !strings.Contains(line, "--network none") || !strings.Contains(line, "readonly") {
			t.Fatal("media backup helper exceeded its selected App directory")
		}
		if _, err := io.ReadAll(stdin); err != nil {
			return err
		}
		_, err := stdout.Write(stream.Bytes())
		return err
	})
	file, err := h.svc.Backup(context.Background(), []string{"sonarr", "other"}, filepath.Join(t.TempDir(), "media.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("private media configuration did not use the Docker helper")
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := backup.Open(f, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, entry := range r.Manifest.Apps {
		if entry.ID == "sonarr" && entry.OwnershipNamespace != backup.OwnershipDocker || entry.ID == "other" && entry.OwnershipNamespace != "" {
			t.Fatal("backup did not preserve per-App ownership handling")
		}
	}
	prepared := false
	for _, call := range h.runner.Calls() {
		if strings.HasPrefix(call, "docker image inspect ") || strings.HasPrefix(call, "docker pull ") {
			prepared = true
		}
		if strings.Contains(call, " stop sonarr") && !prepared {
			t.Fatal("App was stopped before the backup helper was prepared")
		}
	}
}
