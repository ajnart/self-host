package project

import (
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ComposeEnvironmentReferences identifies interpolation inputs in YAML values
// and explicit environment sources for Compose configs and secrets.
// Its bool reports whether an assignment is required rather than defaulted.
// Escaped dollars are container literals, not Project environment references.
func ComposeEnvironmentReferences(data []byte) (map[string]bool, error) {
	root, err := decodeCompose(data)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			for key, required := range interpolationReferences(n.Value) {
				keys[key] = keys[key] || required
			}
			return
		}
		if n.Kind == yaml.AliasNode {
			return // ReadFragment expands aliases before this boundary.
		}
		for i, child := range n.Content {
			if n.Kind != yaml.MappingNode || i%2 == 1 {
				walk(child)
			}
		}
	}
	walk(root)
	for _, kind := range []string{"configs", "secrets"} {
		resources := field(root, kind)
		if resources == nil {
			continue
		}
		for i := 1; i < len(resources.Content); i += 2 {
			source := field(resources.Content[i], "environment")
			if source != nil && source.Kind == yaml.ScalarNode && source.Value != "" {
				keys[source.Value] = true
			}
		}
	}
	return keys, nil
}

func interpolationReferences(value string) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < len(value); i++ {
		if value[i] != '$' || i+1 >= len(value) {
			continue
		}
		if value[i+1] == '$' {
			i++
			continue
		}
		start := i + 1
		braced := value[start] == '{'
		if braced {
			start++
		}
		if start >= len(value) || !envLetter(value[start]) {
			continue
		}
		end := start + 1
		for end < len(value) && (envLetter(value[end]) || value[end] >= '0' && value[end] <= '9') {
			end++
		}
		required := true
		if braced && end < len(value) {
			op := end
			if value[op] == ':' {
				op++
			}
			if op < len(value) && (value[op] == '-' || value[op] == '+') {
				required = false
			}
		}
		key := value[start:end]
		keys[key] = keys[key] || required
		i = end - 1
	}
	return keys
}

func envLetter(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
}

// ExpandEnvironmentReferences includes dependencies of interpolated .env
// values. Single-quoted .env assignments are literal, including their dollars.
func (p *Project) ExpandEnvironmentReferences(refs map[string]bool) (map[string]bool, error) {
	data, err := p.ReadHomeFile(".env")
	if err != nil {
		return nil, err
	}
	lines := environmentAssignments(data)
	values := parseEnv(data)
	out := map[string]bool{}
	var pending []string
	for key, required := range refs {
		out[key] = required
		pending = append(pending, key)
	}
	for len(pending) > 0 {
		key := pending[0]
		pending = pending[1:]
		_, raw, found := strings.Cut(lines[key], "=")
		if !found || strings.HasPrefix(strings.TrimSpace(raw), "'") {
			continue
		}
		for dep, required := range interpolationReferences(values[key]) {
			previous, exists := out[dep]
			if !exists || required && !previous {
				out[dep] = previous || required
				pending = append(pending, dep)
			}
		}
	}
	return out, nil
}

func environmentAssignments(data []byte) map[string]string {
	lines := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, found := strings.Cut(trimmed, "=")
		if found {
			key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
			lines[key] = line
		}
	}
	return lines
}

// EnvironmentAssignmentMatches conservatively compares the saved syntax of
// one assignment, including quoting and interpolation. Parsed values alone
// cannot distinguish a literal dollar expression from an interpolated one.
// Callers must retain the current assignment when it is shared by other Apps.
func (p *Project) EnvironmentAssignmentMatches(source *Project, key string) (bool, error) {
	current, err := p.ReadHomeFile(".env")
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	archived, err := source.ReadHomeFile(".env")
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	currentLine, currentFound := environmentAssignments(current)[key]
	archivedLine, archivedFound := environmentAssignments(archived)[key]
	if !currentFound || !archivedFound {
		return currentFound == archivedFound, nil
	}
	_, currentValue, _ := strings.Cut(currentLine, "=")
	_, archivedValue, _ := strings.Cut(archivedLine, "=")
	return strings.TrimSpace(currentValue) == strings.TrimSpace(archivedValue), nil
}

// RestoreEnvironmentAssignments preserves quoting and interpolation semantics
// for selected archived inputs, and keeps unrelated assignments and comments.
func (p *Project) RestoreEnvironmentAssignments(source *Project, keys []string) error {
	archived, err := source.ReadHomeFile(".env")
	if err != nil {
		return err
	}
	current, err := p.ReadHomeFile(".env")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	assignments := environmentAssignments(archived)
	selected := map[string]bool{}
	for _, key := range keys {
		if _, exists := assignments[key]; exists {
			selected[key] = true
		}
	}
	var out strings.Builder
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(current), "\n"), "\n") {
		key, _, found := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		if found && selected[key] {
			line = assignments[key]
			seen[key] = true
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	sort.Strings(keys)
	for _, key := range keys {
		if selected[key] && !seen[key] {
			out.WriteString(assignments[key])
			out.WriteByte('\n')
		}
	}
	return p.WriteHomeFile(".env", []byte(out.String()), 0o600)
}
