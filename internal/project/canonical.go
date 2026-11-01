package project

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Ownership lives in an ordinary Compose extension. It is independent of
// comments, leaves service definitions editable, and groups multi-service Apps.
const ownershipKey = "x-self-host"
const namesOwner = "@easy-names"

func mapping() *yaml.Node        { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
func scalar(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func put(n *yaml.Node, key string, value *yaml.Node) {
	if len(n.Content) == 0 {
		n.Style &^= yaml.FlowStyle
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, scalar(key), value)
}
func drop(n *yaml.Node, key string) {
	if n == nil {
		return
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}
func object(n *yaml.Node, key string) *yaml.Node {
	child := field(n, key)
	if child == nil {
		child = mapping()
		put(n, key, child)
	}
	return child
}
func decodeCompose(data []byte) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return mapping(), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errors.New("invalid Compose YAML; fix its syntax before changing the Project")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("Compose must be one YAML mapping")
	}
	root := doc.Content[0]
	if err := uniqueKeys(root); err != nil {
		return nil, err
	}
	if err := validateComposeShape(root); err != nil {
		return nil, err
	}
	return root, nil
}
func uniqueKeys(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			if seen[n.Content[i].Value] {
				return errors.New("Compose contains duplicate mapping keys")
			}
			seen[n.Content[i].Value] = true
		}
	}
	for _, child := range n.Content {
		if err := uniqueKeys(child); err != nil {
			return err
		}
	}
	return nil
}

// App IDs become directory components for Backup and removal. A Compose
// extension must never turn an ID into a path outside its Project.
func safeAppID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.IsLocal(id) && !strings.ContainsAny(id, "/\\")
}
func validAppID(id string) error {
	if !safeAppID(id) {
		return errors.New("App ID must be one local directory name")
	}
	return nil
}

func validateComposeShape(root *yaml.Node) error {
	for _, kind := range []string{"services", "volumes", "networks", "configs", "secrets", ownershipKey} {
		if node := field(root, kind); node != nil && node.Kind != yaml.MappingNode {
			return fmt.Errorf("Compose %s must be a mapping", kind)
		}
	}
	services := field(root, "services")
	if services != nil {
		for i := 0; i < len(services.Content); i += 2 {
			if services.Content[i].Kind != yaml.ScalarNode || !safeAppID(services.Content[i].Value) {
				return errors.New("Compose service name must be one local App ID")
			}
		}
	}
	apps := field(field(root, ownershipKey), "apps")
	if apps == nil {
		return nil
	}
	if apps.Kind != yaml.MappingNode {
		return errors.New("Compose App ownership must be a mapping")
	}
	owners := map[string]bool{}
	for i := 0; i < len(apps.Content); i += 2 {
		id := apps.Content[i].Value
		if apps.Content[i].Kind != yaml.ScalarNode || !safeAppID(id) {
			return errors.New("Compose App ownership has an unsafe App ID")
		}
		record := apps.Content[i+1]
		if record.Kind != yaml.MappingNode {
			return errors.New("Compose App ownership record must be a mapping")
		}
		folder := field(record, "folder")
		if folder == nil || folder.Kind != yaml.ScalarNode || (folder.Value != "apps" && folder.Value != ExtraDirName && !(id == namesOwner && folder.Value == "access")) {
			return errors.New("Compose App ownership has an invalid folder")
		}
		if id == namesOwner && folder.Value != "access" {
			return errors.New("Easy names ownership must use its access folder")
		}
		names := field(record, "services")
		if names == nil || names.Kind != yaml.SequenceNode {
			return errors.New("Compose App ownership services must be a list")
		}
		if id == namesOwner && (len(names.Content) != 1 || names.Content[0].Value != "access-caddy") {
			return errors.New("Easy names ownership must contain only its Caddy service")
		}
		for _, name := range names.Content {
			if name.Kind != yaml.ScalarNode || !safeAppID(name.Value) || owners[name.Value] {
				return errors.New("Compose service has ambiguous App ownership")
			}
			owners[name.Value] = true
		}
		resources := field(record, "resources")
		if resources != nil {
			if resources.Kind != yaml.MappingNode {
				return errors.New("Compose resource ownership must be a mapping")
			}
			for j := 0; j < len(resources.Content); j += 2 {
				kind := resources.Content[j].Value
				if resources.Content[j].Kind != yaml.ScalarNode || (kind != "volumes" && kind != "networks" && kind != "configs" && kind != "secrets") {
					return errors.New("Compose resource ownership has an invalid resource kind")
				}
				list := resources.Content[j+1]
				if list.Kind != yaml.SequenceNode {
					return errors.New("Compose resource ownership must contain lists")
				}
				seen := map[string]bool{}
				for _, name := range list.Content {
					if name.Kind != yaml.ScalarNode || name.Value == "" || seen[name.Value] {
						return errors.New("Compose resource ownership must contain distinct resource names")
					}
					seen[name.Value] = true
				}
			}
		}
	}
	return nil
}

// Encoding a surviving alias whose anchor was deleted would corrupt the live
// document. Refuse that edit and leave the original bytes available to fix.
func validateAnchors(root *yaml.Node) error {
	present := map[*yaml.Node]bool{}
	var collect func(*yaml.Node)
	collect = func(n *yaml.Node) {
		present[n] = true
		for _, child := range n.Content {
			collect(child)
		}
	}
	collect(root)
	var check func(*yaml.Node) error
	check = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode && !present[n.Alias] {
			return errors.New("Compose edit would remove an anchor used by another service; update that reference first")
		}
		for _, child := range n.Content {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	return check(root)
}

func encodeCompose(root *yaml.Node) ([]byte, error) {
	if err := validateAnchors(root); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func (p *Project) readRoot() (*yaml.Node, error) {
	data, err := os.ReadFile(p.Path("compose.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return mapping(), nil
	}
	if err != nil {
		return nil, err
	}
	return decodeCompose(data)
}
func canonicalRoot(root *yaml.Node) bool {
	return field(root, "include") == nil && field(root, "services") != nil
}
func (p *Project) Canonical() bool {
	root, err := p.readRoot()
	return err == nil && canonicalRoot(root)
}
func appOwners(root *yaml.Node) *yaml.Node { return object(object(root, ownershipKey), "apps") }
func ownerRecord(root *yaml.Node, id string) *yaml.Node {
	return field(field(field(root, ownershipKey), "apps"), id)
}
func ownedServices(root *yaml.Node, id string) []string {
	record := ownerRecord(root, id)
	var names []string
	if list := field(record, "services"); list != nil {
		for _, n := range list.Content {
			names = append(names, n.Value)
		}
	}
	if record == nil && field(field(root, "services"), id) != nil && serviceOwner(root, id) == "" {
		names = append(names, id)
	}
	return names
}
func serviceOwner(root *yaml.Node, service string) string {
	apps := field(field(root, ownershipKey), "apps")
	if apps == nil {
		return ""
	}
	for i := 0; i < len(apps.Content); i += 2 {
		names := field(apps.Content[i+1], "services")
		if names == nil {
			continue
		}
		for _, name := range names.Content {
			if name.Value == service {
				return apps.Content[i].Value
			}
		}
	}
	return ""
}
func (p *Project) IsExtra(id string) bool {
	if !safeAppID(id) {
		return false
	}
	root, err := p.readRoot()
	if err == nil && canonicalRoot(root) {
		if record := ownerRecord(root, id); record != nil {
			folder := field(record, "folder")
			return folder != nil && folder.Value == ExtraDirName
		}
		if field(field(root, "services"), id) != nil {
			return serviceOwner(root, id) == ""
		}
		return p.legacyIsExtra(id)
	}
	return p.legacyIsExtra(id)
}
func (p *Project) AppIDs() ([]string, error) {
	root, err := p.readRoot()
	if err != nil {
		return nil, err
	}
	if !canonicalRoot(root) {
		return p.legacyAppIDs()
	}
	seen := map[string]bool{}
	var ids []string
	services := field(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, errors.New("Compose services must be a mapping")
	}
	for i := 0; i < len(services.Content); i += 2 {
		name := services.Content[i].Value
		id := serviceOwner(root, name)
		if id == namesOwner {
			continue
		}
		if id == "" {
			id = name
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// Newly supplied extra/ folders are visible to planning before the next
	// approved mutation imports them. Existing root-owned Apps never reread a
	// recovery snapshot.
	legacy, err := p.legacyAppIDs()
	if err != nil {
		return nil, err
	}
	for _, id := range legacy {
		if !seen[id] && p.legacyIsExtra(id) && ownerRecord(root, id) == nil {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func (p *Project) ReadCompose() ([]byte, error) { return os.ReadFile(p.Path("compose.yml")) }
func (p *Project) WriteCompose(data []byte) error {
	if _, err := decodeCompose(data); err != nil {
		return err
	}
	return p.writeFile(p.Path("compose.yml"), data, 0o644)
}
func extractApp(root *yaml.Node, id string) (*yaml.Node, error) {
	fragment := mapping()
	services := mapping()
	for _, name := range ownedServices(root, id) {
		if service := field(field(root, "services"), name); service != nil {
			put(services, name, service)
		}
	}
	if len(services.Content) == 0 {
		return nil, os.ErrNotExist
	}
	put(fragment, "services", services)
	resources := field(ownerRecord(root, id), "resources")
	if resources != nil {
		for i := 0; i < len(resources.Content); i += 2 {
			kind := resources.Content[i].Value
			owned := mapping()
			for _, name := range resources.Content[i+1].Content {
				if value := field(field(root, kind), name.Value); value != nil {
					put(owned, name.Value, value)
				}
			}
			if len(owned.Content) > 0 {
				put(fragment, kind, owned)
			}
		}
	}
	// A person may add a volume/network/config/secret directly in the root
	// file. Carry referenced definitions in scoped Backup snapshots as well.
	for i := 1; i < len(services.Content); i += 2 {
		var service map[string]any
		if err := services.Content[i].Decode(&service); err != nil {
			return nil, errors.New("cannot resolve App Compose resources")
		}
		for _, kind := range []string{"volumes", "networks", "configs", "secrets"} {
			for _, name := range referencedResources(service[kind], kind) {
				if definition := field(field(root, kind), name); definition != nil {
					put(object(fragment, kind), name, definition)
				}
			}
		}
		if service["networks"] == nil && service["network_mode"] == nil {
			if definition := field(field(root, "networks"), "default"); definition != nil {
				put(object(fragment, "networks"), "default", definition)
			}
		}
	}
	return fragment, nil
}
func referencedResources(value any, kind string) []string {
	var names []string
	switch entries := value.(type) {
	case map[string]any:
		for name := range entries {
			names = append(names, name)
		}
	case []any:
		for _, entry := range entries {
			switch item := entry.(type) {
			case string:
				name := item
				if kind == "volumes" {
					source, _, found := splitVolume(item)
					if !found || bindSource(source) {
						continue
					}
					name = source
				}
				names = append(names, name)
			case map[string]any:
				if kind == "volumes" && item["type"] != "volume" {
					continue
				}
				if source, ok := item["source"].(string); ok {
					names = append(names, source)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}
func (p *Project) ReadFragment(id string) ([]byte, error) {
	if err := validAppID(id); err != nil {
		return nil, err
	}
	root, err := p.readRoot()
	if err != nil {
		return nil, err
	}
	if !canonicalRoot(root) {
		return os.ReadFile(p.FragmentPath(id))
	}
	fragment, err := extractApp(root, id)
	if errors.Is(err, os.ErrNotExist) && ownerRecord(root, id) == nil && p.legacyIsExtra(id) {
		return os.ReadFile(p.Path(ExtraDirName, id, "compose.yml"))
	}
	if err != nil {
		return nil, err
	}
	return encodeCompose(expandAliases(fragment))
}

func expandAliases(n *yaml.Node) *yaml.Node { return copyExpanded(n, map[*yaml.Node]bool{}) }
func copyExpanded(n *yaml.Node, parents map[*yaml.Node]bool) *yaml.Node {
	// Cyclic aliases are not usable App snapshots. Keep the reference so
	// validateAnchors returns a safe error rather than recursing indefinitely.
	if parents[n] {
		clone := *n
		clone.Content = nil
		return &clone
	}
	parents[n] = true
	defer delete(parents, n)
	if n.Kind == yaml.AliasNode {
		return copyExpanded(n.Alias, parents)
	}
	clone := *n
	clone.Anchor = ""
	clone.Content = nil
	for _, child := range n.Content {
		clone.Content = append(clone.Content, copyExpanded(child, parents))
	}
	return &clone
}

// NormalizeFragment returns the same root-relative App representation that
// ReadFragment returns after WriteFragment. It never modifies Project files.
func (p *Project) NormalizeFragment(id, content string) ([]byte, error) {
	if err := validAppID(id); err != nil {
		return nil, err
	}
	fragment, err := decodeCompose([]byte(content))
	if err != nil {
		return nil, err
	}
	if err := p.prepareFragment(fragment, filepath.Join("apps", id), true); err != nil {
		return nil, err
	}
	return encodeCompose(fragment)
}
func (p *Project) prepareFragment(fragment *yaml.Node, base string, defaults bool) error {
	if field(fragment, "include") != nil {
		return errors.New("nested Compose includes require an explicit migration")
	}
	if field(fragment, "name") != nil {
		return errors.New("included Compose project names require explicit migration to preserve storage identities")
	}
	if err := p.rebaseCompose(fragment, base); err != nil {
		return err
	}
	services := field(fragment, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return errors.New("App Compose services must be a mapping")
	}
	for i := 0; i < len(services.Content); i += 2 {
		service := services.Content[i+1]
		if service.Kind != yaml.MappingNode {
			return errors.New("Compose service must be a mapping")
		}
		if defaults && field(service, "logging") == nil {
			restart := field(service, "restart")
			if restart != nil && restart.Value != "no" && restart.Value != "" {
				logging := mapping()
				put(logging, "driver", scalar("local"))
				options := mapping()
				put(options, "max-size", scalar("10m"))
				put(options, "max-file", scalar("3"))
				put(logging, "options", options)
				put(service, "logging", logging)
			}
		}
	}
	return nil
}
func (p *Project) WriteFragment(id, content string) error {
	if err := validAppID(id); err != nil {
		return err
	}
	if err := p.ValidateFragment(id, content); err != nil {
		return err
	}
	if p.IsExtra(id) {
		return fmt.Errorf("%s is an Unmanaged app; edit its service in compose.yml", id)
	}
	fragment, err := decodeCompose([]byte(content))
	if err != nil {
		return err
	}
	if err := p.prepareFragment(fragment, filepath.Join("apps", id), true); err != nil {
		return err
	}
	if err := p.WriteRoot(); err != nil {
		return err
	}
	return p.writeCanonicalFragment(id, fragment, "apps", []byte(FragmentHeader(id)+content))
}

// WriteCanonicalFragment restores a root-relative App snapshot without
// rebasing paths a second time. The caller holds the Project mutation lock.
func (p *Project) WriteCanonicalFragment(id, content string) error {
	if err := validAppID(id); err != nil {
		return err
	}
	return p.RestoreFragment(id, content, p.AppFolder(id), true)
}

// RestoreFragment writes an App's saved section with explicit ownership.
// Legacy snapshots need paths rebased once; canonical snapshots already use
// Project-relative paths. It preserves unrelated services and resources.
func (p *Project) RestoreFragment(id, content, folder string, canonical bool) error {
	if err := validAppID(id); err != nil {
		return err
	}
	if folder != "apps" && folder != ExtraDirName {
		return errors.New("invalid restored App folder")
	}
	fragment, err := decodeCompose([]byte(content))
	if err != nil {
		return err
	}
	if field(fragment, "include") != nil {
		return errors.New("App snapshots cannot include Compose files")
	}
	services := field(fragment, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return errors.New("App snapshot requires Compose services")
	}
	for i := 1; i < len(services.Content); i += 2 {
		if services.Content[i].Kind != yaml.MappingNode {
			return errors.New("App snapshot services must be mappings")
		}
	}
	if !canonical {
		if err := p.prepareFragment(fragment, filepath.Join(folder, id), false); err != nil {
			return err
		}
	}
	if err := p.WriteRoot(); err != nil {
		return err
	}
	return p.writeCanonicalFragment(id, fragment, folder, nil)
}

func (p *Project) writeCanonicalFragment(id string, fragment *yaml.Node, folder string, archive []byte) error {
	root, err := p.readRoot()
	if err != nil {
		return err
	}
	if archive == nil && folder == ExtraDirName && ownerRecord(root, id) == nil && field(field(root, "services"), id) != nil && serviceOwner(root, id) == "" {
		record := mapping()
		put(record, "folder", scalar(folder))
		put(record, "services", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{scalar(id)}})
		put(appOwners(root), id, record)
	}
	if err := mergeApp(root, id, folder, fragment); err != nil {
		return err
	}
	data, err := encodeCompose(root)
	if err != nil {
		return err
	}
	if err := p.MkdirAll(folder, id); err != nil {
		return err
	}
	if err := p.WriteCompose(data); err != nil {
		return err
	}
	// Legacy locations are recovery snapshots, never live authority.
	if archive != nil {
		return p.writeFile(p.FragmentPath(id), archive, 0o644)
	}
	return nil
}

func equivalent(a, b *yaml.Node) bool {
	var left, right any
	if a.Decode(&left) != nil || b.Decode(&right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
func resourceExclusivelyOwned(root *yaml.Node, id, kind, name string) bool {
	apps := field(field(root, ownershipKey), "apps")
	owned := false
	if apps == nil {
		return false
	}
	for i := 0; i < len(apps.Content); i += 2 {
		list := field(field(apps.Content[i+1], "resources"), kind)
		if list == nil {
			continue
		}
		for _, resource := range list.Content {
			if resource.Value == name {
				if apps.Content[i].Value != id {
					return false
				}
				owned = true
			}
		}
	}
	return owned
}

func mergeApp(root *yaml.Node, id, folder string, fragment *yaml.Node) error {
	services := object(root, "services")
	incoming := field(fragment, "services")
	for i := 0; i < len(incoming.Content); i += 2 {
		name := incoming.Content[i].Value
		if field(services, name) != nil && serviceOwner(root, name) != id {
			return fmt.Errorf("Compose service %s already belongs to another App or a manual service", name)
		}
	}
	record := mapping()
	put(record, "folder", scalar(folder))
	names := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	resources := mapping()
	for i := 0; i < len(fragment.Content); i += 2 {
		kind := fragment.Content[i].Value
		if kind == "services" || kind == "name" || kind == "version" {
			continue
		}
		if kind != "volumes" && kind != "networks" && kind != "configs" && kind != "secrets" {
			if current := field(root, kind); current != nil && !equivalent(current, fragment.Content[i+1]) {
				return fmt.Errorf("Compose field %s conflicts with the Project", kind)
			}
			put(root, kind, fragment.Content[i+1])
			continue
		}
		defs := fragment.Content[i+1]
		if defs.Kind != yaml.MappingNode {
			return errors.New("Compose resources must be mappings")
		}
		target := object(root, kind)
		list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for j := 0; j < len(defs.Content); j += 2 {
			name := defs.Content[j].Value
			if old := field(target, name); old != nil && !equivalent(old, defs.Content[j+1]) && !resourceExclusivelyOwned(root, id, kind, name) {
				return fmt.Errorf("Compose %s resource %s conflicts with an existing definition", kind, name)
			}
			put(target, name, defs.Content[j+1])
			list.Content = append(list.Content, scalar(name))
		}
		put(resources, kind, list)
	}
	for _, old := range ownedServices(root, id) {
		drop(services, old)
	}
	for i := 0; i < len(incoming.Content); i += 2 {
		name := incoming.Content[i].Value
		put(services, name, incoming.Content[i+1])
		names.Content = append(names.Content, scalar(name))
	}
	put(record, "services", names)
	if len(resources.Content) > 0 {
		put(record, "resources", resources)
	}
	put(appOwners(root), id, record)
	return nil
}
func (p *Project) RemoveFragment(id string) error {
	if err := validAppID(id); err != nil {
		return err
	}
	if err := p.WriteRoot(); err != nil {
		return err
	}
	root, err := p.readRoot()
	if err != nil {
		return err
	}
	for _, name := range ownedServices(root, id) {
		drop(field(root, "services"), name)
	}
	drop(appOwners(root), id)
	// Keep shared/global resources: manual services may depend on them and
	// ordinary down never deletes their data.
	data, err := encodeCompose(root)
	if err != nil {
		return err
	}
	if err := p.WriteCompose(data); err != nil {
		return err
	}
	if err := os.Remove(p.FragmentPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func (p *Project) HasNames() bool {
	root, err := p.readRoot()
	if err == nil && canonicalRoot(root) {
		return len(ownedServices(root, namesOwner)) > 0
	}
	return p.legacyHasNames()
}
func (p *Project) WriteNamesFragment(content string) error {
	if p.Canonical() && p.HasNames() {
		return nil
	}
	fragment, err := decodeCompose([]byte(content))
	if err != nil {
		return err
	}
	if err := p.prepareFragment(fragment, NamesFolder, true); err != nil {
		return err
	}
	if err := p.WriteRoot(); err != nil {
		return err
	}
	root, err := p.readRoot()
	if err != nil {
		return err
	}
	if err := mergeApp(root, namesOwner, "access", fragment); err != nil {
		return err
	}
	data, err := encodeCompose(root)
	if err != nil {
		return err
	}
	if err := p.WriteCompose(data); err != nil {
		return err
	}
	return p.writeLegacyNamesFragment(content)
}
func (p *Project) RemoveNames() error {
	if p.HasNames() {
		if err := p.RemoveFragment(namesOwner); err != nil {
			return err
		}
	}
	return p.removeLegacyNames()
}

// WriteRoot upgrades the include layout once. Once services are canonical it
// only imports genuinely new extra/ folders and never re-reads App snapshots.
func (p *Project) WriteRoot() error {
	root, err := p.readRoot()
	if err != nil {
		return err
	}
	canonical := canonicalRoot(root)
	var before []byte
	if canonical {
		before, err = encodeCompose(root)
		if err != nil {
			return err
		}
	}
	if !canonical {
		includes := field(root, "include")
		var paths []string
		if includes != nil {
			if includes.Kind != yaml.SequenceNode {
				return errors.New("Compose include must be a list for automatic migration")
			}
			for _, entry := range includes.Content {
				if entry.Kind != yaml.ScalarNode || strings.Contains(entry.Value, "$") || !filepath.IsLocal(entry.Value) {
					return errors.New("complex Compose includes require explicit migration; Project left unchanged")
				}
				paths = append(paths, entry.Value)
			}
		} else {
			ids, err := p.legacyAppIDs()
			if err != nil {
				return err
			}
			for _, id := range ids {
				paths = append(paths, filepath.Join(p.AppFolder(id), id, "compose.yml"))
			}
			if p.legacyHasNames() {
				paths = append(paths, filepath.Join(NamesFolder, "compose.yml"))
			}
		}
		for _, path := range paths {
			path = filepath.Clean(path)
			parts := strings.Split(filepath.ToSlash(path), "/")
			id, folder := "", ""
			if len(parts) == 3 && (parts[0] == "apps" || parts[0] == ExtraDirName) && parts[2] == "compose.yml" {
				id, folder = parts[1], parts[0]
			} else if filepath.ToSlash(path) == NamesFolder+"/compose.yml" {
				id, folder = namesOwner, "access"
			} else {
				return errors.New("unrecognized Compose include requires explicit migration; Project left unchanged")
			}
			content, err := os.ReadFile(p.Path(path))
			if err != nil {
				return err
			}
			fragment, err := decodeCompose(content)
			if err != nil {
				return err
			}
			if err := p.prepareFragment(fragment, filepath.Dir(path), false); err != nil {
				return err
			}
			if err := mergeApp(root, id, folder, fragment); err != nil {
				return err
			}
		}
		drop(root, "include")
		drop(root, "version")
		object(root, "services")
	}
	if field(root, "name") == nil || !canonical {
		name := p.ComposeName
		if name == "" {
			name = Name
		}
		put(root, "name", scalar(name))
	}
	// The legacy extra/ entry point remains supported for newly supplied Apps.
	legacy, err := p.legacyAppIDs()
	if err != nil {
		return err
	}
	for _, id := range legacy {
		if !p.legacyIsExtra(id) || ownerRecord(root, id) != nil || field(field(root, "services"), id) != nil {
			continue
		}
		data, err := os.ReadFile(p.Path(ExtraDirName, id, "compose.yml"))
		if err != nil {
			return err
		}
		fragment, err := decodeCompose(data)
		if err != nil {
			return err
		}
		if err := p.prepareFragment(fragment, filepath.Join(ExtraDirName, id), false); err != nil {
			return err
		}
		if err := mergeApp(root, id, ExtraDirName, fragment); err != nil {
			return err
		}
	}
	data, err := encodeCompose(root)
	if err != nil {
		return err
	}
	// Validate the entire migration before touching either live file.
	secrets, err := p.LoadSecrets()
	if err != nil {
		return err
	}
	if !canonical {
		if original, err := os.ReadFile(p.Path("compose.yml")); err == nil {
			if _, err := os.Stat(p.Path("compose.legacy.yml")); errors.Is(err, os.ErrNotExist) {
				if err := p.writeFile(p.Path("compose.legacy.yml"), original, 0o600); err != nil {
					return err
				}
			}
		}
	}
	if err := p.updateEnv(secrets, nil); err != nil {
		return err
	}
	if canonical && bytes.Equal(before, data) {
		return nil
	}
	return p.WriteCompose(data)
}

// RestoreComposeBase retains a canonical Backup's global resources and user
// extensions for a fresh Project. Only separately selected App snapshots are
// restored as services; a partial Backup never installs unrelated services.
func (p *Project) RestoreComposeBase(data []byte) error {
	current, err := p.readRoot()
	if err != nil {
		return err
	}
	if services := field(current, "services"); services != nil && len(services.Content) > 0 {
		return errors.New("Compose base restoration requires an empty Project")
	}
	root, err := decodeCompose(data)
	if err != nil {
		return err
	}
	if !canonicalRoot(root) {
		return errors.New("Compose base restoration requires a canonical Backup")
	}
	put(root, "services", mapping())
	if metadata := field(root, ownershipKey); metadata != nil {
		drop(metadata, "apps")
	}
	name := p.ComposeName
	if name == "" {
		name = Name
	}
	put(root, "name", scalar(name))
	restored, err := encodeCompose(root)
	if err != nil {
		return err
	}
	return p.WriteCompose(restored)
}

// StoredComposeName is the identity ordinary Compose uses for this canonical
// Project. Legacy include roots hardcoded selfhost even when the CLI overrode
// it, so they deliberately return no stored identity before migration.
func (p *Project) StoredComposeName() (string, error) {
	root, err := p.readRoot()
	if err != nil {
		return "", err
	}
	if !canonicalRoot(root) {
		return "", nil
	}
	name := ""
	if node := field(root, "name"); node != nil {
		if node.Kind != yaml.ScalarNode {
			return "", errors.New("Compose project name must be a string")
		}
		name = node.Value
	}
	values, err := p.loadEnv(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if override := values["COMPOSE_PROJECT_NAME"]; override != "" {
		name = override
	}
	if name == "" {
		return "", nil
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-')
		if !valid {
			return "", errors.New("canonical Compose project name must be a literal lowercase name; preserve the existing Docker project identity before changing it")
		}
	}
	return name, nil
}
