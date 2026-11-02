package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
)

type restoreResourceApp struct{ *testApp }

func (a restoreResourceApp) Compose(rc app.RenderContext) string {
	return a.testApp.Compose(rc) + "    volumes: [store:/data]\nvolumes:\n  store:\n    external: true\n    name: archived-store\n"
}

func TestRestoreSharedResourceConflictPreservesDataAndContainers(t *testing.T) {
	h := newHarness(t, restoreResourceApp{&testApp{id: "one", done: map[string]bool{}}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	if err := p.WriteAppFile("one", "config/db", []byte("archived-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := h.svc.Backup(context.Background(), []string{"one"}, filepath.Join(t.TempDir(), "one.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := p.ReadCompose()
	current := strings.Replace(string(root), "services:\n", "services:\n  manual:\n    image: example/manual\n    volumes: [store:/data]\n", 1)
	current = strings.ReplaceAll(current, "archived-store", "current-store")
	if err := p.WriteCompose([]byte(current)); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAppFile("one", "config/db", []byte("current-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeCalls := len(h.runner.Calls())
	err = h.svc.Restore(context.Background(), file, []string{"one"}, "")
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("shared resource change was not rejected: %v", err)
	}
	for _, call := range h.runner.Calls()[beforeCalls:] {
		if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
			t.Fatal("resource conflict changed containers")
		}
	}
	root, _ = p.ReadCompose()
	if string(root) != current {
		t.Fatal("resource conflict changed Compose")
	}
	data, _ := os.ReadFile(p.Path("apps", "one", "config", "db"))
	if string(data) != "current-data" {
		t.Fatal("resource conflict replaced existing App data")
	}
}
