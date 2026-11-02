package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreEnvironmentBackedResourcePreservesSelectedAndUnrelatedInputs(t *testing.T) {
	for _, kind := range []string{"secrets", "configs"} {
		for _, shared := range []bool{false, true} {
			name := "selected"
			if shared {
				name = "shared-conflict"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
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
				service := fmt.Sprintf("  manual:\n    image: example/manual:v1\n    %s: [manual-input]\n", kind)
				if shared {
					service += fmt.Sprintf("  neighbor:\n    image: example/neighbor:v1\n    %s: [manual-input]\n", kind)
				}
				current := strings.Replace(string(root), "services:\n", "services:\n"+service, 1)
				current += fmt.Sprintf("%s:\n  manual-input:\n    environment: RESOURCE_INPUT\n", kind)
				if err := p.WriteCompose([]byte(current)); err != nil {
					t.Fatal(err)
				}
				values, err := p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				values["RESOURCE_INPUT"] = "fixture-archived-input"
				values["UNRELATED_INPUT"] = "fixture-unrelated-archived"
				if err := p.SaveSecrets(values); err != nil {
					t.Fatal(err)
				}
				file, err := h.svc.Backup(context.Background(), []string{"manual"}, filepath.Join(t.TempDir(), "manual.tar.zst"), "")
				if err != nil {
					t.Fatal(err)
				}
				values["RESOURCE_INPUT"] = "fixture-current-input"
				values["UNRELATED_INPUT"] = "fixture-unrelated-current"
				if err := p.SaveSecrets(values); err != nil {
					t.Fatal(err)
				}
				beforeCalls := len(h.runner.Calls())
				err = h.svc.Restore(context.Background(), file, []string{"manual"}, "")
				want := "fixture-archived-input"
				if shared {
					want = "fixture-current-input"
					if err == nil || !strings.Contains(err.Error(), "used by another App") {
						t.Fatalf("shared environment-backed resource should fail preflight: %v", err)
					}
					for _, call := range h.runner.Calls()[beforeCalls:] {
						if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
							t.Fatal("shared resource conflict changed containers")
						}
					}
				} else if err != nil {
					t.Fatal(err)
				}
				after, err := p.LoadSecrets()
				if err != nil {
					t.Fatal(err)
				}
				if after["RESOURCE_INPUT"] != want || after["UNRELATED_INPUT"] != "fixture-unrelated-current" {
					t.Fatal("Restore did not preserve selected and unrelated resource inputs")
				}
			})
		}
	}
}
