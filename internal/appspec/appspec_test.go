package appspec_test

import (
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/appspec"
)

const valid = `id: demo
name: Demo
category: Tools
description: A demo
image: demo/demo:latest
docs: https://example.com
port: 8080
container_port: 80
compose: |
  services:
    demo:
      image: {{.Image}}
      ports:
        - ${BIND_IP}:{{.Port}}:{{.ContainerPort}}
setup:
  kind: no-account
  probe:
    path: /
`

func TestParse(t *testing.T) {
	a, err := appspec.Parse("demo.yaml", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Info().Service; got != "demo" {
		t.Errorf("Service = %q, want the id", got)
	}
	want := "services:\n  demo:\n    image: demo/demo:latest\n    ports:\n      - ${BIND_IP}:9000:80\n"
	if got := a.Compose(app.RenderContext{Port: 9000}); got != want {
		t.Errorf("Compose =\n%s\nwant\n%s", got, want)
	}

	for _, tc := range []struct{ name, file, from, to, want string }{
		{"unknown field", "demo.yaml", "port: 8080", "port: 8080\nprot: 1", "prot"},
		{"missing description", "demo.yaml", "description: A demo\n", "", "description is required"},
		{"missing setup", "demo.yaml", "setup:\n  kind: no-account\n  probe:\n    path: /\n", "", "setup"},
		{"unknown setup kind", "demo.yaml", "kind: no-account", "kind: magic", "unknown setup kind"},
		{"file name differs from id", "other.yaml", "", "", "must be named demo.yaml"},
		{"bad id", "Demo.yaml", "id: demo", "id: Demo", "id"},
		{"port out of range", "demo.yaml", "port: 8080", "port: 70000", "between 1 and 65535"},
		{"missing service", "demo.yaml", "    demo:", "    other:", `no service "demo"`},
		{"template error", "demo.yaml", "{{.Image}}", "{{.Nope", "compose"},
		{"unsafe file path", "demo.yaml", "setup:", "files:\n  - path: ../x\nsetup:", "inside the App folder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := appspec.Parse(tc.file, []byte(strings.Replace(valid, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
