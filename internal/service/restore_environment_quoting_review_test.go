package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestorePreservesUnrelatedConsumersOfQuotedEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, archived, current string
		conflict                bool
	}{
		{"literal archive", "TOKEN='${BASE}'", "TOKEN=${BASE}", true},
		{"interpolated archive", "TOKEN=${BASE}", "TOKEN='${BASE}'", true},
		{"equivalent shared assignment", "TOKEN=${BASE}", "export TOKEN = ${BASE}", false},
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
			services := "  manual:\n    image: example/manual\n    environment: {TOKEN: '${TOKEN}'}\n  neighbor:\n    image: example/neighbor\n    environment: {TOKEN: '${TOKEN}'}\n"
			if err := p.WriteCompose([]byte(strings.Replace(string(root), "services:\n", "services:\n"+services, 1))); err != nil {
				t.Fatal(err)
			}
			env, err := p.ReadHomeFile(".env")
			if err != nil {
				t.Fatal(err)
			}
			archived := append(append([]byte{}, env...), []byte(tc.archived+"\nBASE=fixture-base-value\n")...)
			if err := p.WriteHomeFile(".env", archived, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := h.svc.Backup(context.Background(), []string{"manual"}, filepath.Join(t.TempDir(), "manual.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			current := append(append([]byte{}, env...), []byte(tc.current+"\nBASE=fixture-base-value\n")...)
			if err := p.WriteHomeFile(".env", current, 0o600); err != nil {
				t.Fatal(err)
			}
			beforeCalls := len(h.runner.Calls())
			err = h.svc.Restore(context.Background(), file, []string{"manual"}, "")
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "used by another App") {
					t.Fatalf("shared quoting change must fail preflight: %v", err)
				}
				for _, call := range h.runner.Calls()[beforeCalls:] {
					if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
						t.Fatal("shared quoting conflict changed containers")
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := p.ReadHomeFile(".env")
			if err != nil || string(after) != string(current) {
				t.Fatalf("Restore changed an unrelated consumer's saved environment syntax: %v", err)
			}
		})
	}
}
