package project

import (
	"fmt"
	"os"
	"slices"
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
	if services := field(root, "services"); services != nil {
		for i := 1; i < len(services.Content); i += 2 {
			environment := field(services.Content[i], "environment")
			if environment == nil {
				continue
			}
			if environment.Kind == yaml.MappingNode {
				for j := 0; j < len(environment.Content); j += 2 {
					if environment.Content[j+1].Tag == "!!null" {
						key := environment.Content[j].Value
						if _, found := keys[key]; !found {
							keys[key] = false
						}
					}
				}
			} else if environment.Kind == yaml.SequenceNode {
				for _, entry := range environment.Content {
					if entry.Kind == yaml.ScalarNode && entry.Value != "" && !strings.Contains(entry.Value, "=") {
						if _, found := keys[entry.Value]; !found {
							keys[entry.Value] = false
						}
					}
				}
			}
		}
	}
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
	return expandEnvironmentReferences(environmentEntries(data), refs), nil
}

func expandEnvironmentReferences(entries []environmentEntry, refs map[string]bool) map[string]bool {
	out := map[string]bool{}
	var pending []string
	for key, required := range refs {
		out[key] = required
		pending = append(pending, key)
	}
	for len(pending) > 0 {
		key := pending[0]
		pending = pending[1:]
		for _, entry := range entries {
			_, raw, _ := strings.Cut(entry.text, "=")
			if entry.key != key || strings.HasPrefix(strings.TrimSpace(raw), "'") {
				continue
			}
			for dep, required := range interpolationReferences(entry.value) {
				previous, exists := out[dep]
				if !exists || required && !previous {
					out[dep] = previous || required
					pending = append(pending, dep)
				}
			}
		}
	}
	return out
}

type environmentDeclaration struct {
	key, value string
}

func environmentAssignmentContext(entries []environmentEntry, key string) []environmentDeclaration {
	refs := expandEnvironmentReferences(entries, map[string]bool{key: true})
	var declarations []environmentDeclaration
	for _, entry := range entries {
		if _, referenced := refs[entry.key]; referenced {
			_, raw, _ := strings.Cut(entry.text, "=")
			declarations = append(declarations, environmentDeclaration{entry.key, strings.TrimSpace(raw)})
		}
	}
	return declarations
}

func environmentAssignments(data []byte) map[string]string {
	lines := map[string]string{}
	for _, entry := range environmentEntries(data) {
		if entry.key != "" {
			lines[entry.key] = entry.text
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
	return slices.Equal(environmentAssignmentContext(environmentEntries(current), key), environmentAssignmentContext(environmentEntries(archived), key)), nil
}

// RestoreEnvironmentAssignments preserves quoting and interpolation semantics
// for selected archived inputs, and keeps unrelated assignments and comments.
func (p *Project) RestoreEnvironmentAssignments(source *Project, keys []string) error {
	data, err := p.restoredEnvironmentAssignments(source, keys)
	if err != nil || data == nil {
		return err
	}
	return p.WriteHomeFile(".env", data, 0o600)
}

func (p *Project) ValidateEnvironmentAssignments(source *Project, keys []string) error {
	_, err := p.restoredEnvironmentAssignments(source, keys)
	return err
}

func (p *Project) restoredEnvironmentAssignments(source *Project, keys []string) ([]byte, error) {
	archived, err := source.ReadHomeFile(".env")
	if err != nil {
		return nil, err
	}
	current, err := p.ReadHomeFile(".env")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	selected := map[string]bool{}
	assignments := environmentAssignments(archived)
	for _, key := range keys {
		if _, exists := assignments[key]; exists {
			selected[key] = true
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	archivedEntries := environmentEntries(archived)
	currentEntries := environmentEntries(current)
	var incoming, existing []environmentEntry
	for _, entry := range archivedEntries {
		if selected[entry.key] {
			incoming = append(incoming, entry)
		}
	}
	for _, entry := range currentEntries {
		if selected[entry.key] {
			existing = append(existing, entry)
		}
	}
	unchanged := slices.EqualFunc(existing, incoming, func(a, b environmentEntry) bool { return a.text == b.text })
	var out strings.Builder
	inserted := false
	insert := func() {
		for _, entry := range incoming {
			out.WriteString(entry.text)
			out.WriteByte('\n')
		}
		inserted = true
	}
	for _, entry := range currentEntries {
		if !unchanged && selected[entry.key] {
			if !inserted {
				insert()
			}
			continue
		}
		out.WriteString(entry.text)
		out.WriteByte('\n')
	}
	if !unchanged && !inserted {
		insert()
	}
	merged := []byte(out.String())
	if unchanged {
		merged = current
	}
	mergedEntries := environmentEntries(merged)
	checked := map[string]bool{}
	for _, entry := range append(currentEntries, incoming...) {
		key := entry.key
		if key == "" || checked[key] {
			continue
		}
		checked[key] = true
		expected := currentEntries
		if selected[key] {
			expected = archivedEntries
		}
		if !slices.Equal(environmentAssignmentContext(expected, key), environmentAssignmentContext(mergedEntries, key)) {
			return nil, fmt.Errorf("restoring Project environment assignment %s would change its declaration order or interpolation dependencies; migrate shared assignments explicitly before Restore", key)
		}
	}
	if unchanged {
		return nil, nil
	}
	return merged, nil
}

// Import missing legacy assignments before comparing values for a write. Raw
// syntax matters: quoting an interpolated legacy value makes it a literal.
func (p *Project) legacyEnvironment(data []byte) ([]byte, error) {
	if p.Canonical() {
		return data, nil
	}
	legacy, err := os.ReadFile(p.Path("secrets.env"))
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return nil, err
	}
	current := environmentAssignments(data)
	var extra strings.Builder
	for _, entry := range environmentEntries(legacy) {
		if entry.key == "" || publicSetting(entry.key) {
			continue
		}
		if _, exists := current[entry.key]; !exists {
			extra.WriteString(entry.text)
			extra.WriteByte('\n')
		}
	}
	if extra.Len() == 0 {
		return data, nil
	}
	merged := append([]byte(nil), data...)
	if len(merged) > 0 && merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	return append(merged, extra.String()...), nil
}

func (p *Project) syncLegacySecrets() error {
	data, err := os.ReadFile(p.Path(".env"))
	if err != nil {
		return err
	}
	var out strings.Builder
	out.WriteString("# Private credentials; the live Compose environment is .env.\n")
	for _, entry := range environmentEntries(data) {
		if entry.key != "" && !publicSetting(entry.key) {
			out.WriteString(entry.text)
			out.WriteByte('\n')
		}
	}
	return p.writeFile(p.Path("secrets.env"), []byte(out.String()), 0o600)
}
