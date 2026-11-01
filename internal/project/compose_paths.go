package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Rebase only fields Compose resolves against the included file's directory.
// Container paths and Dockerfile paths (relative to their build context) stay
// untouched. Unsupported file composition fails before writing live records.
func (p *Project) rebaseCompose(root *yaml.Node, base string) error {
	values, err := p.loadEnv(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var pathError error
	fixPath := func(path string) string {
		if err := safeInterpolatedPath(path, values); err != nil {
			pathError = err
		}
		return rebasePath(base, path)
	}
	fix := func(n *yaml.Node) {
		if n != nil && n.Kind == yaml.ScalarNode {
			n.Value = fixPath(n.Value)
		}
	}
	if containsYAMLReference(root) {
		return errors.New("Compose fragments with YAML aliases or merge keys require explicit migration; Project left unchanged")
	}
	services := field(root, "services")
	if services != nil {
		for i := 0; i < len(services.Content); i += 2 {
			service := services.Content[i+1]
			if field(service, "extends") != nil {
				return errors.New("Compose extends requires explicit migration; Project left unchanged")
			}
			for _, key := range []string{"env_file", "label_file"} {
				if err := rebaseFileList(field(service, key), fix); err != nil {
					return err
				}
			}
			if volumes := field(service, "volumes"); volumes != nil {
				for _, volume := range volumes.Content {
					if volume.Kind == yaml.ScalarNode {
						source, tail, found := splitVolume(volume.Value)
						if found && bindSource(source) {
							volume.Value = fixPath(source) + ":" + tail
						}
					} else if kind := field(volume, "type"); kind != nil && kind.Value == "bind" {
						fix(field(volume, "source"))
					}
				}
			}
			if devices := field(service, "devices"); devices != nil {
				for _, device := range devices.Content {
					if device.Kind == yaml.ScalarNode {
						source, tail, found := strings.Cut(device.Value, ":")
						if found {
							device.Value = fixPath(source) + ":" + tail
						} else {
							device.Value = fixPath(source)
						}
					}
				}
			}
			if build := field(service, "build"); build != nil {
				if build.Kind == yaml.ScalarNode {
					fix(build)
				} else {
					fix(field(build, "context"))
					if field(build, "context") == nil {
						put(build, "context", scalar("./"+filepath.ToSlash(base)))
					}
					if contexts := field(build, "additional_contexts"); contexts != nil {
						if contexts.Kind == yaml.MappingNode {
							for j := 1; j < len(contexts.Content); j += 2 {
								fix(contexts.Content[j])
							}
						} else {
							for _, entry := range contexts.Content {
								name, path, ok := strings.Cut(entry.Value, "=")
								if ok {
									entry.Value = name + "=" + fixPath(path)
								}
							}
						}
					}
					// Host SSH socket paths are not build context paths.
					if ssh := field(build, "ssh"); ssh != nil {
						for _, entry := range ssh.Content {
							name, path, ok := strings.Cut(entry.Value, "=")
							if ok {
								entry.Value = name + "=" + fixPath(path)
							}
						}
					}
				}
			}
			if credential := field(service, "credential_spec"); credential != nil {
				fix(field(credential, "file"))
			}
			if develop := field(service, "develop"); develop != nil {
				if watch := field(develop, "watch"); watch != nil {
					for _, entry := range watch.Content {
						fix(field(entry, "path"))
					}
				}
			}
			if options := field(service, "security_opt"); options != nil {
				for _, entry := range options.Content {
					kind, path, ok := strings.Cut(entry.Value, "=")
					if ok && (kind == "seccomp" || kind == "apparmor") && strings.HasPrefix(path, ".") {
						entry.Value = kind + "=" + fixPath(path)
					}
				}
			}
		}
	}
	for _, kind := range []string{"configs", "secrets"} {
		if resources := field(root, kind); resources != nil {
			for i := 1; i < len(resources.Content); i += 2 {
				fix(field(resources.Content[i], "file"))
			}
		}
	}
	if volumes := field(root, "volumes"); volumes != nil {
		for i := 1; i < len(volumes.Content); i += 2 {
			options := field(volumes.Content[i], "driver_opts")
			device := field(options, "device")
			if device != nil && strings.HasPrefix(device.Value, ".") {
				return fmt.Errorf("relative volume driver device requires explicit migration")
			}
		}
	}
	return pathError
}
func containsYAMLReference(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || n.Value == "<<" {
		return true
	}
	for _, child := range n.Content {
		if containsYAMLReference(child) {
			return true
		}
	}
	return false
}
func rebaseFileList(n *yaml.Node, fix func(*yaml.Node)) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		fix(n)
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return errors.New("Compose file paths must be strings or lists")
	}
	for _, entry := range n.Content {
		if entry.Kind == yaml.ScalarNode {
			fix(entry)
		} else if entry.Kind == yaml.MappingNode {
			fix(field(entry, "path"))
		} else {
			return errors.New("invalid Compose file path")
		}
	}
	return nil
}
func rebasePath(base, path string) string {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "~") || strings.HasPrefix(path, "$") || strings.Contains(path, "://") || strings.HasPrefix(path, "git@") || strings.HasPrefix(path, "service:") || strings.HasPrefix(path, "docker-image:") {
		return path
	}
	return "./" + filepath.ToSlash(filepath.Clean(filepath.Join(base, path)))
}
func bindSource(source string) bool {
	return strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") || strings.HasPrefix(source, "$") || strings.Contains(source, "/")
}

// A colon inside ${VAR:-default} is interpolation syntax, not a mount separator.
func splitVolume(value string) (string, string, bool) {
	depth := 0
	for i, r := range value {
		if r == '{' {
			depth++
		}
		if r == '}' && depth > 0 {
			depth--
		}
		if r == ':' && depth == 0 {
			return value[:i], value[i+1:], true
		}
	}
	return value, "", false
}
func safeInterpolatedPath(path string, values map[string]string) error {
	if !strings.HasPrefix(path, "$") {
		return nil
	}
	variable := strings.TrimPrefix(path, "$")
	if strings.HasPrefix(variable, "{") {
		end := strings.IndexByte(variable, '}')
		if end < 0 {
			return errors.New("invalid interpolation in Compose file path")
		}
		variable = variable[1:end]
	} else {
		end := 0
		for end < len(variable) && ((variable[end] >= 'A' && variable[end] <= 'Z') || (variable[end] >= 'a' && variable[end] <= 'z') || (variable[end] >= '0' && variable[end] <= '9') || variable[end] == '_') {
			end++
		}
		variable = variable[:end]
	}
	key, fallback, hasFallback := strings.Cut(variable, ":-")
	if !hasFallback {
		key, fallback, hasFallback = strings.Cut(variable, "-")
	}
	value := values[key]
	if value == "" && hasFallback {
		value = fallback
	}
	if !filepath.IsAbs(value) && !strings.HasPrefix(value, "~") {
		return errors.New("interpolated Compose file paths must resolve to absolute locations before migration")
	}
	return nil
}
