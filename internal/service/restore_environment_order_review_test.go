package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopedRestorePreservesEnvironmentDeclarationContexts(t *testing.T) {
	for _, tc := range []struct {
		name, archived, current, selected, neighbor, refusal string
	}{
		{"passthrough dependent", "BASE=abc\n# keep this\nDERIVED=${BASE}/keep\n", "BASE=abc\n# keep this\nDERIVED=${BASE}/keep\n", "{BASE: '${BASE}'}", "[DERIVED]", ""},
		{"duplicate dependency", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "{TOKEN: '${TOKEN}'}", "{BASE: '${BASE}'}", ""},
		{"passthrough selected", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "[TOKEN]", "[BASE]", ""},
		{"changed duplicate slot", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nTOKEN=current\nBASE=last\n", "[TOKEN]", "[BASE]", ""},
		{"App secret duplicate slot", "BASE=first\nMANUAL_TOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nMANUAL_TOKEN=current\nBASE=last\n", "{STATIC: value}", "[BASE]", ""},
		{"changed passthrough dependency", "BASE=abc\nDERIVED=${BASE}/keep\n", "BASE=changed\nDERIVED=${BASE}/keep\n", "{BASE: '${BASE}'}", "[DERIVED]", "used by another App"},
		{"changed earlier duplicate", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=changed\nTOKEN=${BASE}/token\nBASE=last\n", "{TOKEN: '${TOKEN}'}", "{BASE: '${BASE}'}", "used by another App"},
		{"missing duplicate slot", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nBASE=last\n", "{TOKEN: '${TOKEN}'}", "[BASE]", "declaration order or interpolation dependencies"},
		{"missing App secret slot", "BASE=first\nMANUAL_TOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nBASE=last\n", "{STATIC: value}", "[BASE]", "declaration order or interpolation dependencies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			p, err := h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			root, err := p.ReadCompose()
			if err != nil {
				t.Fatal(err)
			}
			manual := "  manual:\n    image: example/manual\n    environment: " + tc.selected + "\n  neighbor:\n    image: example/neighbor\n    environment: " + tc.neighbor + "\n"
			if err := p.WriteCompose([]byte(strings.Replace(string(root), "services:\n", "services:\n"+manual, 1))); err != nil {
				t.Fatal(err)
			}
			env, err := p.ReadHomeFile(".env")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.WriteHomeFile(".env", append(env, []byte(tc.archived)...), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := h.svc.Backup(context.Background(), []string{"manual"}, filepath.Join(t.TempDir(), "manual.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.WriteHomeFile(".env", append(env, []byte(tc.current)...), 0o600); err != nil {
				t.Fatal(err)
			}
			before := map[string]string{}
			for _, name := range []string{".env", "compose.yml", "state.json"} {
				data, err := p.ReadHomeFile(name)
				if err != nil {
					t.Fatal(err)
				}
				before[name] = string(data)
			}
			calls := len(h.runner.Calls())
			err = h.svc.Restore(context.Background(), file, []string{"manual"}, "")
			if tc.refusal == "" {
				if err != nil {
					t.Fatal(err)
				}
				data, err := p.ReadHomeFile(".env")
				if err != nil || string(data) != string(env)+tc.archived {
					t.Fatalf("Restore changed declaration contexts or comments: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("ambiguous environment was not refused: %v", err)
			}
			for name, original := range before {
				data, err := p.ReadHomeFile(name)
				if err != nil || string(data) != original {
					t.Fatalf("refused Restore modified %s: %v", name, err)
				}
			}
			if _, err := os.Lstat(p.Path("restore-startup.json")); !os.IsNotExist(err) {
				t.Fatal("refused Restore wrote its startup plan")
			}
			for _, call := range h.runner.Calls()[calls:] {
				if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
					t.Fatal("refused Restore changed containers")
				}
			}
		})
	}
}
