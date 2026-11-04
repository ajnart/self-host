// Package appspec builds an App from a declarative YAML spec: its metadata, a
// Compose template and a setupspec Setup. An App that needs nothing the spec
// language lacks is one YAML file and an icon, with no Go and no registration.
package appspec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/setupspec"
	"go.yaml.in/yaml/v3"
)

type spec struct {
	app.Info        `yaml:",inline"`
	Inputs          []app.Input      `yaml:"inputs"`
	Secrets         []app.SecretSpec `yaml:"secrets"`
	Files           []app.File       `yaml:"files"`
	StandaloneGuide string           `yaml:"standalone_guide"`
	Compose         string           `yaml:"compose"`
	Setup           setupspec.Spec   `yaml:"setup"`
}

// renderData is what the compose and file templates can name. Host settings
// such as ${BIND_IP} are not template fields: they stay literal for Compose.
type renderData struct {
	Image         string
	Port          int
	ContainerPort int
	Inputs        map[string]string
}

// App is an App built from a spec. It implements every optional interface a
// spec can declare and returns nothing for what the spec leaves out.
type App struct{ spec spec }

var (
	_ app.App             = App{}
	_ app.SecretDeclarer  = App{}
	_ app.FileDeclarer    = App{}
	_ app.StandaloneGuide = App{}
)

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Load parses every *.yaml file in fsys, in name order.
func Load(fsys fs.FS) ([]app.App, error) {
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, err
	}
	apps := make([]app.App, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		a, err := Parse(name, data)
		if err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, nil
}

// Parse validates one spec; name is its file name, which must be <id>.yaml.
func Parse(name string, data []byte) (app.App, error) {
	a, err := parse(name, data)
	if err != nil {
		return nil, fmt.Errorf("app spec %s: %w", name, err)
	}
	return a, nil
}

func parse(name string, data []byte) (App, error) {
	var s spec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return App{}, errors.New("file is empty")
		}
		return App{}, err
	}
	if s.Service == "" {
		s.Service = s.ID
	}
	if s.ContainerPort == 0 {
		s.ContainerPort = s.Port
	}
	if err := s.validate(); err != nil {
		return App{}, err
	}
	if want := s.ID + ".yaml"; name != want {
		return App{}, fmt.Errorf("file must be named %s", want)
	}
	a := App{spec: s}
	if err := a.dryRun(); err != nil {
		return App{}, err
	}
	return a, nil
}

func (s spec) validate() error {
	if !idPattern.MatchString(s.ID) {
		return fmt.Errorf("id %q must be lowercase letters, digits and single hyphens", s.ID)
	}
	for _, f := range []struct{ field, value string }{
		{"name", s.Name}, {"category", s.Category}, {"description", s.Description},
		{"image", s.Image}, {"docs", s.Docs}, {"compose", s.Compose},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s is required", f.field)
		}
	}
	if s.Port < 1 || s.Port > 65535 || s.ContainerPort < 1 || s.ContainerPort > 65535 {
		return fmt.Errorf("port %d and container_port %d must be between 1 and 65535", s.Port, s.ContainerPort)
	}
	if err := s.Setup.Validate(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	seen := map[string]bool{}
	for _, in := range s.Inputs {
		if in.Key == "" || seen[in.Key] {
			return fmt.Errorf("input key %q is empty or repeated", in.Key)
		}
		seen[in.Key] = true
	}
	seen = map[string]bool{}
	for _, sec := range s.Secrets {
		if sec.Name == "" || sec.Length < 1 || seen[sec.Name] {
			return fmt.Errorf("secret %q needs a unique name and a length", sec.Name)
		}
		seen[sec.Name] = true
	}
	seen = map[string]bool{}
	for _, f := range s.Files {
		if !fs.ValidPath(f.Path) || f.Path == "." || seen[f.Path] {
			return fmt.Errorf("file path %q must be a unique path inside the App folder", f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

// dryRun renders every template once with the default port, so a bad template
// or a Compose without the main service fails when the binary builds.
func (a App) dryRun() error {
	compose, err := a.render("compose", a.spec.Compose, app.RenderContext{Port: a.spec.Port}, nil)
	if err != nil {
		return fmt.Errorf("compose: %w", err)
	}
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return fmt.Errorf("compose is not valid YAML: %w", err)
	}
	if _, ok := doc.Services[a.spec.Service]; !ok {
		return fmt.Errorf("compose has no service %q", a.spec.Service)
	}
	for _, f := range a.spec.Files {
		if _, err := a.render(f.Path, f.Content, app.RenderContext{Port: a.spec.Port}, func(string) string { return "" }); err != nil {
			return fmt.Errorf("file %s: %w", f.Path, err)
		}
	}
	return nil
}

// render fills text with rc. Files get a secret function; Compose gets none,
// since it names secrets by their .env variables.
func (a App) render(name, text string, rc app.RenderContext, secret func(string) string) (string, error) {
	funcs := template.FuncMap{}
	if secret != nil {
		funcs["secret"] = secret
	}
	t, err := template.New(name).Option("missingkey=zero").Funcs(funcs).Parse(text)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	data := renderData{
		Image:         a.spec.Image,
		Port:          rc.Port,
		ContainerPort: a.spec.ContainerPortFor(rc.Port),
		Inputs:        rc.Inputs,
	}
	if err := t.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (a App) Info() app.Info { return a.spec.Info }

func (a App) Inputs() []app.Input { return slices.Clone(a.spec.Inputs) }

func (a App) Secrets() []app.SecretSpec { return slices.Clone(a.spec.Secrets) }

func (a App) StandaloneGuide() string { return a.spec.StandaloneGuide }

func (a App) Links() []app.Link { return nil }

// Compose panics on a render error: dryRun already rendered the template, so
// one here means an input or setting the template cannot take.
func (a App) Compose(rc app.RenderContext) string {
	out, err := a.render("compose", a.spec.Compose, rc, nil)
	if err != nil {
		panic(fmt.Sprintf("app spec %s: compose: %v", a.spec.ID, err))
	}
	return out
}

func (a App) Files(rc app.RenderContext, secret func(string) string) []app.File {
	files := slices.Clone(a.spec.Files)
	for i := range files {
		content, err := a.render(files[i].Path, files[i].Content, rc, secret)
		if err != nil {
			panic(fmt.Sprintf("app spec %s: file %s: %v", a.spec.ID, files[i].Path, err))
		}
		files[i].Content = content
	}
	return files
}

func (a App) Setup(ctx context.Context, h *helpers.H) error { return a.spec.Setup.Setup(ctx, h) }

func (a App) Check(ctx context.Context, h *helpers.H) error { return a.spec.Setup.Check(ctx, h) }
