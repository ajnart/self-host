package project

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type FixturePolicy struct {
	Root    string
	Service string
	Image   string
	Images  map[string]string
}

type FixturePort struct {
	Port     int
	Protocol string
}

type FixtureCompose struct {
	Services []string
	Ports    []FixturePort
	Images   map[string]string
}

var fixtureImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:-]*(?:@sha256:[a-f0-9]{64})?$`)

func ValidFixtureImage(image string) bool {
	return image != "" && len(image) <= 512 && fixtureImage.MatchString(image) && !strings.Contains(image, "://")
}

func fixturePlain(n *yaml.Node) bool {
	if n == nil || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return false
	}
	for _, c := range n.Content {
		if !fixturePlain(c) {
			return false
		}
	}
	return true
}

func (p *Project) PrepareFixtureCompose(policy FixturePolicy) (FixtureCompose, error) {
	var out FixtureCompose
	if !filepath.IsAbs(policy.Root) || !fixtureInside(policy.Root, p.Root) {
		return out, errors.New("testkit requires its owned canonical Project")
	}
	for _, path := range []string{p.Root, p.Path("compose.yml"), p.Path(".env")} {
		if !fixtureNoSymlink(policy.Root, path) {
			return out, errors.New("testkit refuses symlinks in Project configuration")
		}
	}
	if !p.Canonical() {
		return out, errors.New("testkit requires its owned canonical Project")
	}
	for _, name := range []string{"compose.override.yml", "compose.override.yaml", "docker-compose.yml", "docker-compose.yaml", "docker-compose.override.yml", "docker-compose.override.yaml", "compose.yaml"} {
		if _, err := os.Lstat(p.Path(name)); !errors.Is(err, os.ErrNotExist) {
			return out, errors.New("testkit refuses Compose overrides")
		}
	}
	data, err := os.ReadFile(p.Path("compose.yml"))
	if err != nil {
		return out, err
	}
	root, err := decodeCompose(data)
	if err != nil {
		return out, err
	}
	if !fixturePlain(root) {
		return out, errors.New("testkit refuses Compose aliases")
	}
	for i := 0; i < len(root.Content); i += 2 {
		if !slices.Contains([]string{"name", "services", "volumes", "networks", ownershipKey}, root.Content[i].Value) {
			return out, errors.New("testkit refuses unsupported Compose resources")
		}
	}
	for _, kind := range []string{"volumes", "networks"} {
		resources := field(root, kind)
		if resources == nil {
			continue
		}
		if kind == "volumes" && len(resources.Content) != 0 {
			return out, errors.New("testkit refuses named volumes; use private bind mounts")
		}
		for i := 1; i < len(resources.Content); i += 2 {
			r := resources.Content[i]
			if r.Kind != yaml.MappingNode && r.Tag != "!!null" || len(r.Content) != 0 {
				return out, errors.New("testkit refuses external or customized global resources")
			}
		}
	}
	services := field(root, "services")
	if services == nil || len(services.Content) == 0 || len(services.Content) > 10 {
		return out, errors.New("testkit permits one to five containers including dependencies")
	}
	settings, err := p.LoadSettings()
	if err != nil {
		return out, err
	}
	environment, err := p.loadEnv(".env")
	if err != nil {
		return out, err
	}
	for key := range environment {
		if strings.HasPrefix(key, "COMPOSE_") || strings.HasPrefix(key, "DOCKER_") {
			return out, errors.New("testkit refuses Docker and Compose environment overrides")
		}
	}
	if settings["BIND_IP"] != "127.0.0.1" || settings["EXPOSURE"] != "local" {
		return out, errors.New("testkit requires loopback Access")
	}
	out.Images = map[string]string{}
	for i := 0; i < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			return out, errors.New("invalid fixture service")
		}
		for j := 0; j < len(svc.Content); j += 2 {
			if !slices.Contains([]string{"image", "environment", "command", "entrypoint", "ports", "volumes", "depends_on", "restart", "healthcheck", "logging", "user", "working_dir", "hostname", "expose", "init", "stop_grace_period", "cpus", "mem_limit", "pids_limit"}, svc.Content[j].Value) {
				return out, errors.New("testkit refuses unprovable service configuration")
			}
		}
		image := field(svc, "image")
		if override, ok := policy.Images[name]; ok {
			if !ValidFixtureImage(override) {
				return out, errors.New("invalid selected image")
			}
			image = scalar(override)
			put(svc, "image", image)
		}
		if name == policy.Service && policy.Image != "" {
			if !ValidFixtureImage(policy.Image) {
				return out, errors.New("invalid selected image")
			}
			image = scalar(policy.Image)
			put(svc, "image", image)
		}
		if image == nil || image.Kind != yaml.ScalarNode || !ValidFixtureImage(image.Value) {
			return out, errors.New("testkit requires explicit images")
		}
		out.Images[name] = image.Value
		out.Services = append(out.Services, name)
		if deps := field(svc, "depends_on"); deps != nil {
			for j, node := range deps.Content {
				if deps.Kind == yaml.MappingNode && j%2 != 0 {
					continue
				}
				if field(services, node.Value) == nil {
					return out, errors.New("fixture dependency outside Project")
				}
			}
		}
		if mounts := field(svc, "volumes"); mounts != nil {
			if mounts.Kind != yaml.SequenceNode {
				return out, errors.New("unsupported fixture mounts")
			}
			for _, mount := range mounts.Content {
				source, target, mode, err := fixtureMount(mount)
				if err != nil {
					return out, err
				}
				if !strings.HasPrefix(target, "/") || strings.Contains(strings.ToLower(source+":"+target), "docker.sock") {
					return out, errors.New("unsafe fixture mount")
				}
				for _, key := range []string{"DATA_ROOT", "MEDIA_ROOT", "DOWNLOADS_ROOT"} {
					source = strings.ReplaceAll(source, "${"+key+"}", settings[key])
				}
				if !strings.ContainsAny(source, "/.$") {
					return out, errors.New("testkit refuses named volumes; use private bind mounts")
				}
				if !filepath.IsAbs(source) {
					source = p.Path(source)
				}
				if strings.Contains(source, "$") || !fixtureInside(policy.Root, source) || !fixtureNoSymlink(policy.Root, source) {
					return out, errors.New("fixture bind escapes private root")
				}
				if mode != "ro" && mode != "rw" {
					return out, errors.New("unsupported fixture bind mode")
				}
			}
		}
		if ports := field(svc, "ports"); ports != nil {
			if ports.Kind != yaml.SequenceNode {
				return out, errors.New("unsupported fixture publications")
			}
			for _, port := range ports.Content {
				if port.Kind != yaml.ScalarNode {
					return out, errors.New("unsupported fixture publication")
				}
				text := strings.ReplaceAll(port.Value, "${BIND_IP}", "127.0.0.1")
				parts := strings.Split(text, ":")
				if len(parts) != 3 || parts[0] != "127.0.0.1" {
					return out, errors.New("fixture publication must use loopback")
				}
				publication, err := strconv.Atoi(parts[1])
				if err != nil || publication < 1 || publication > 65535 {
					return out, errors.New("unsupported fixture host port")
				}
				target, protocol, _ := strings.Cut(parts[2], "/")
				if protocol == "" {
					protocol = "tcp"
				}
				containerPort, err := strconv.Atoi(target)
				if err != nil || containerPort < 1 || containerPort > 65535 || protocol != "tcp" && protocol != "udp" {
					return out, errors.New("unsupported fixture port")
				}
				out.Ports = append(out.Ports, FixturePort{Port: publication, Protocol: protocol})
			}
		}
		put(svc, "cpus", scalar("1.0"))
		put(svc, "mem_limit", scalar("1g"))
		put(svc, "pids_limit", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "512"})
	}
	if policy.Image != "" && out.Images[policy.Service] == "" {
		return out, errors.New("selected service absent")
	}
	put(root, "name", scalar(p.ComposeName))
	data, err = encodeCompose(root)
	if err != nil {
		return out, err
	}
	return out, p.writeFile(p.Path("compose.yml"), data, 0o600)
}

func fixtureMount(mount *yaml.Node) (source, target, mode string, err error) {
	if mount.Kind == yaml.ScalarNode {
		parts := strings.Split(mount.Value, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return "", "", "", errors.New("unsafe fixture mount")
		}
		mode = "rw"
		if len(parts) == 3 {
			mode = parts[2]
		}
		return parts[0], parts[1], mode, nil
	}
	if mount.Kind != yaml.MappingNode {
		return "", "", "", errors.New("unsupported fixture mount")
	}
	mode = "rw"
	for i := 0; i+1 < len(mount.Content); i += 2 {
		key, value := mount.Content[i].Value, mount.Content[i+1]
		switch {
		case key == "type" && value.Kind == yaml.ScalarNode && value.Value == "bind":
		case key == "source" && value.Kind == yaml.ScalarNode:
			source = value.Value
		case key == "target" && value.Kind == yaml.ScalarNode:
			target = value.Value
		case key == "read_only" && value.Kind == yaml.ScalarNode && (value.Value == "true" || value.Value == "false"):
			if value.Value == "true" {
				mode = "ro"
			}
		case key == "bind" && value.Kind == yaml.MappingNode && len(value.Content) == 2 && value.Content[0].Value == "create_host_path" && (value.Content[1].Value == "true" || value.Content[1].Value == "false"):
		default:
			return "", "", "", errors.New("unsupported fixture mount")
		}
	}
	if field(mount, "type") == nil || source == "" || target == "" {
		return "", "", "", errors.New("unsupported fixture mount")
	}
	return source, target, mode, nil
}

func fixtureInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func fixtureNoSymlink(root, path string) bool {
	for current := filepath.Clean(path); fixtureInside(root, current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 || err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if current == filepath.Clean(root) {
			return true
		}
	}
	return false
}
