package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestFreshRestoreKeepsGlobalAliasesOfServiceAnchors(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	root, _ := p.ReadCompose()
	current := strings.Replace(string(root), "  one:\n", "  one: &one-template\n", 1)
	current += "x-compat-template: *one-template # keep the template\n"
	if err := p.WriteCompose([]byte(current)); err != nil {
		t.Fatal(err)
	}
	file, err := h.svc.Backup(context.Background(), nil, filepath.Join(t.TempDir(), "all.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.Root); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
		t.Fatal(err)
	}
	root, _ = p.ReadCompose()
	var model map[string]any
	if err := yaml.Unmarshal(root, &model); err != nil {
		t.Fatal(err)
	}
	template, ok := model["x-compat-template"].(map[string]any)
	if !ok || template["image"] != "example/test:latest" || !strings.Contains(string(root), "# keep the template") {
		t.Fatalf("fresh Restore lost the global template or its comment: template=%v, comment=%t", template, strings.Contains(string(root), "# keep the template"))
	}
}

func TestScopedRestoreKeepsMultilineEnvironmentValues(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	root, _ := p.ReadCompose()
	manual := "  manual:\n    image: example/manual\n    environment:\n      TOKEN: ${MANUAL_TOKEN}\n"
	if err := p.WriteCompose([]byte(strings.Replace(string(root), "services:\n", "services:\n"+manual, 1))); err != nil {
		t.Fatal(err)
	}
	env, _ := p.ReadHomeFile(".env")
	archived := "MANUAL_TOKEN='archived first\narchived last' # keep the quotation\n"
	if err := p.WriteHomeFile(".env", append(env, []byte(archived)...), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := h.svc.Backup(context.Background(), []string{"manual"}, filepath.Join(t.TempDir(), "manual.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	unrelated := "UNRELATED='current first\ncurrent last'\n"
	if err := p.WriteHomeFile(".env", append(env, []byte("MANUAL_TOKEN='new first\nnew last'\n"+unrelated)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Restore(context.Background(), file, []string{"manual"}, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := p.ReadHomeFile(".env")
	if !strings.HasSuffix(string(data), unrelated+archived) {
		t.Fatal("scoped Restore corrupted the selected or unrelated multiline value")
	}
}
