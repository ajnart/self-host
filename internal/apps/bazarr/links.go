package bazarr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/mediastorage"
	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
)

type libraryConnection struct {
	id, name             string
	port                 int
	collection, mappings string
}

func libraryConnections() []libraryConnection {
	return []libraryConnection{
		{id: "sonarr", name: "Sonarr", port: 8989, collection: "series", mappings: "path_mappings"},
		{id: "radarr", name: "Radarr", port: 7878, collection: "movies", mappings: "path_mappings_movie"},
	}
}

type connectionFields struct {
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	BaseURL string `json:"base_url"`
	SSL     bool   `json:"ssl"`
	APIKey  string `json:"apikey"`
}

type connectionSnapshot struct {
	fields   connectionFields
	enabled  bool
	mappings []any
	other    string
}

func (s libraryConnection) manual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: reason + " Review " + h.URL + " > Settings > " + s.name + ". Preserve existing connections, profiles, providers, schedules and path mappings, then run self-host setup bazarr. Restore known matching ownership through an approved Project Backup when necessary; never claim a matching Address or clear uncertain ownership without confirming the attempted change is absent or restored."}
}

func (s libraryConnection) link() app.Link {
	return app.NewLink("link-"+s.id, s.id, "verify the "+s.name+" library connection",
		app.LinkStep{
			Message: "verify the " + s.name + " library connection",
			Do:      func(ctx context.Context, from, to *helpers.H) error { return s.apply(ctx, from, to) },
		},
		&app.LinkStep{
			Message: "restore only the owned " + s.name + " connection settings",
			Check: func(_ context.Context, from, _ *helpers.H) (bool, error) {
				return from.Secret(s.ownershipKey(from)) == "", nil
			},
			Do: func(ctx context.Context, from, to *helpers.H) error { return s.remove(ctx, from, to) },
		})
}

func readSettingsValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("settings are too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := map[string]any{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, errors.New("invalid settings key")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("duplicate settings key")
			}
			value, err := readSettingsValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		_, err = decoder.Token()
		return object, err
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := readSettingsValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = decoder.Token()
		return array, err
	default:
		if _, delimiter := token.(json.Delim); delimiter {
			return nil, errors.New("invalid settings value")
		}
		return token, nil
	}
}

func settingsDocument(raw []byte) (any, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("oversized settings object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	value, err := readSettingsValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing settings data")
	}
	return value, nil
}

func settingsObject(raw []byte) (map[string]any, error) {
	value, err := settingsDocument(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("missing settings object")
	}
	return object, nil
}

func readConnectionFields(block map[string]any) (connectionFields, bool) {
	ip, ipOK := block["ip"].(string)
	port, portOK := block["port"].(json.Number)
	portNumber, portErr := port.Int64()
	base, baseOK := block["base_url"].(string)
	ssl, sslOK := block["ssl"].(bool)
	key, keyOK := block["apikey"].(string)
	valid := ipOK && portOK && portErr == nil && portNumber >= 1 && portNumber <= 65535 && baseOK && sslOK && keyOK
	return connectionFields{ip, int(portNumber), base, ssl, key}, valid
}

func (s libraryConnection) request(ctx context.Context, from *helpers.H, form url.Values) (helpers.Response, error) {
	key := from.Secret(from.Key("api_key"))
	if key == "" {
		return helpers.Response{}, s.manual(from, "Bazarr's accepted API key is missing; repair its own Setup first.")
	}
	from.Log.Secret(key)
	request := helpers.Request{Path: "/api/system/settings", NoRedirect: true, Header: map[string]string{"X-API-KEY": key}}
	if form != nil {
		request.Method, request.Form = http.MethodPost, form
	}
	response, err := from.HTTP(ctx, request)
	if err != nil {
		return response, errors.New("Bazarr settings request did not complete; retry without replacing credentials or ownership")
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return response, s.manual(from, "Bazarr rejected its saved API key. Repair self-host setup bazarr without replacing existing authentication.")
	}
	return response, nil
}

func (s libraryConnection) snapshot(ctx context.Context, from *helpers.H) (connectionSnapshot, error) {
	var snapshot connectionSnapshot
	response, err := s.request(ctx, from, nil)
	if err != nil {
		return snapshot, err
	}
	if response.Status != http.StatusOK {
		return snapshot, fmt.Errorf("read Bazarr settings returned HTTP %d; no settings were changed", response.Status)
	}
	settings, err := settingsObject(response.Body)
	if err != nil {
		return snapshot, s.manual(from, "Bazarr settings are malformed or ambiguous; existing settings were left unchanged.")
	}
	block, blockOK := settings[s.id].(map[string]any)
	general, generalOK := settings["general"].(map[string]any)
	if !blockOK || !generalOK {
		return snapshot, s.manual(from, "The supported connection or general settings block is missing.")
	}
	fields, fieldsOK := readConnectionFields(block)
	enabled, enabledOK := general["use_"+s.id].(bool)
	mappings, mappingsOK := general[s.mappings].([]any)
	if !fieldsOK || !enabledOK || !mappingsOK {
		return snapshot, s.manual(from, "The supported connection settings, enabled choice or path mappings could not be verified.")
	}
	from.Log.Secret(fields.APIKey)
	snapshot.fields = fields
	snapshot.enabled, snapshot.mappings = enabled, mappings
	for _, field := range []string{"ip", "port", "base_url", "ssl", "apikey"} {
		delete(block, field)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return snapshot, s.manual(from, "Unrelated Bazarr settings could not be verified.")
	}
	snapshot.other = fmt.Sprintf("%x", sha256.Sum256(raw))
	return snapshot, nil
}

func (fields connectionFields) same(other connectionFields) bool {
	fields.BaseURL = strings.TrimSuffix(fields.BaseURL, "/")
	other.BaseURL = strings.TrimSuffix(other.BaseURL, "/")
	return fields == other
}

func (s libraryConnection) vacant(fields connectionFields) bool {
	return fields.same(connectionFields{IP: "127.0.0.1", Port: s.port, BaseURL: "/"})
}

func (s libraryConnection) desired(from, to *helpers.H) (connectionFields, error) {
	key := to.Secret(to.Key("api_key"))
	if from.App != "bazarr" || to.App != s.id || to.Service != s.id || to.ContainerPort < 1 || to.ContainerPort > 65535 || key == "" {
		return connectionFields{}, s.manual(from, "The selected target's internal Address or accepted API key is unavailable; finish self-host setup "+s.id+" first.")
	}
	from.Log.Secret(key)
	to.Log.Secret(key)
	return connectionFields{IP: to.Service, Port: to.ContainerPort, BaseURL: "/", APIKey: key}, nil
}

func (s libraryConnection) emptyLibrary(ctx context.Context, from *helpers.H) error {
	var library struct {
		Data  []json.RawMessage `json:"data"`
		Total *int              `json:"total"`
	}
	key := from.Secret(from.Key("api_key"))
	if err := readAPI(ctx, from, key, "/api/"+s.collection+"?start=0&length=1", &library); err != nil {
		return err
	}
	if library.Data == nil || library.Total == nil || *library.Total < 0 {
		return s.manual(from, "The existing Bazarr library could not be enumerated; the connection must not be claimed.")
	}
	if *library.Total != 0 || len(library.Data) != 0 {
		return s.manual(from, "Bazarr already contains this backend's library. Review its existing connection and ownership instead of replacing or adopting it.")
	}
	return nil
}

type libraryReader func(ctx context.Context, resource string, value any) error

func (s libraryConnection) decode(from *helpers.H, response helpers.Response, value any) error {
	if response.Status != http.StatusOK {
		return s.manual(from, "The selected "+s.name+" rejected an authenticated library read. Repair self-host setup "+s.id+" first; credentials and settings were preserved.")
	}
	if _, err := settingsDocument(response.Body); err != nil {
		return s.manual(from, "The selected target returned malformed, ambiguous or oversized library data; bounded verification could not complete.")
	}
	if response.Decode(value) != nil {
		return s.manual(from, "The selected target returned invalid or oversized library data; bounded verification could not complete.")
	}
	return nil
}

func (s libraryConnection) directReader(from, to *helpers.H) libraryReader {
	return func(ctx context.Context, resource string, value any) error {
		response, err := to.HTTP(ctx, servarr.Request(to, servarr.V3, helpers.Request{Path: resource, NoRedirect: true}))
		if err != nil {
			return fmt.Errorf("the selected %s API could not be reached; retry its own Setup before Bazarr", s.name)
		}
		return s.decode(from, response, value)
	}
}

func (s libraryConnection) consumerReader(from, to *helpers.H) libraryReader {
	return func(ctx context.Context, resource string, value any) error {
		response, err := from.ConsumerGET(ctx, to, servarr.V3+resource, to.Secret(to.Key("api_key")))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return s.manual(from, "Bazarr's own network context could not be admitted to read the selected "+s.name+" (a trusted single Project bridge network, the standard LinuxServer image with curl 8.4+ and GNU timeout, and one running replica of each App are required). The saved connection and all settings were preserved.")
		}
		return s.decode(from, response, value)
	}
}

func libraryPath(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == strings.TrimSuffix(value, "/") && !strings.ContainsAny(value, "\r\n\x00")
}

func (s libraryConnection) verifyLibrary(ctx context.Context, from *helpers.H, read libraryReader) ([]string, error) {
	var status struct {
		Name    string  `json:"appName"`
		Version string  `json:"version"`
		Base    *string `json:"urlBase"`
	}
	if err := read(ctx, "/system/status", &status); err != nil {
		return nil, err
	}
	if !strings.EqualFold(status.Name, s.name) || strings.TrimSpace(status.Version) == "" || status.Base == nil || (*status.Base != "" && *status.Base != "/") {
		return nil, s.manual(from, "The selected target identity or root API base could not be verified. Existing target settings were left unchanged.")
	}
	var roots []struct {
		ID         int64  `json:"id"`
		Path       string `json:"path"`
		Accessible *bool  `json:"accessible"`
	}
	if err := read(ctx, "/rootfolder", &roots); err != nil {
		return nil, err
	}
	if len(roots) == 0 || len(roots) > 32 {
		return nil, s.manual(from, "The selected target has no bounded, verifiable root-folder list. Review its existing media folders; Bazarr does not create or reset them.")
	}
	paths, ids := map[string]bool{}, map[int64]bool{}
	var rootPaths []string
	for _, root := range roots {
		if root.ID < 1 || ids[root.ID] || paths[root.Path] || !libraryPath(root.Path) || root.Accessible == nil || !*root.Accessible {
			return nil, s.manual(from, "A selected target root is malformed, duplicated or inaccessible; existing folders were preserved.")
		}
		ids[root.ID], paths[root.Path] = true, true
		rootPaths = append(rootPaths, root.Path)
	}
	collection := "/series"
	if s.id == "radarr" {
		collection = "/movie"
	}
	var items []struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
		Root string `json:"rootFolderPath"`
	}
	if err := read(ctx, collection, &items); err != nil {
		return nil, err
	}
	if items == nil || len(items) > 128 {
		return nil, s.manual(from, "The selected target returned no explicit or bounded library-item list; no connection write is authorized by that response.")
	}
	itemIDs := map[int64]bool{}
	for _, item := range items {
		inside := false
		for _, root := range rootPaths {
			if strings.HasPrefix(item.Path, strings.TrimSuffix(root, "/")+"/") && (item.Root == "" || strings.TrimSuffix(item.Root, "/") == strings.TrimSuffix(root, "/")) {
				inside = true
			}
		}
		if item.ID < 1 || itemIDs[item.ID] || !libraryPath(item.Path) || !inside {
			return nil, s.manual(from, "A library item does not identify media under the selected existing roots; no roots or files were changed.")
		}
		itemIDs[item.ID] = true
	}
	return rootPaths, nil
}

func (s libraryConnection) verifyTarget(ctx context.Context, from, to *helpers.H, snapshot connectionSnapshot) ([]string, error) {
	if _, err := s.desired(from, to); err != nil {
		return nil, err
	}
	rootPaths, err := s.verifyLibrary(ctx, from, s.directReader(from, to))
	if err != nil {
		return nil, err
	}
	if len(snapshot.mappings) != 0 {
		return nil, s.manual(from, "Custom path mappings need explicit same-media verification. They were preserved; Setup does not invent or rewrite mappings.")
	}
	if from.Config.Exec == nil || to.Config.Exec == nil {
		return nil, s.manual(from, "The effective media mounts cannot be inspected read-only through the installed Apps.")
	}
	for _, root := range rootPaths {
		consumer, err := s.pathIdentity(ctx, from, root)
		if err != nil {
			return nil, s.manual(from, "Bazarr cannot verify read-only access to the selected media root. Review its effective mounts; the selected layout expects "+mediastorage.Media(from.Settings)+" without migrating existing folders.")
		}
		target, err := s.pathIdentity(ctx, to, root)
		if err != nil || consumer != target {
			return nil, s.manual(from, "The selected root does not resolve to the same observed directory in both Apps. Review effective mounts and application-user access; matching path text alone is insufficient.")
		}
	}
	return rootPaths, nil
}

func (s libraryConnection) pathIdentity(ctx context.Context, h *helpers.H, root string) (string, error) {
	result, err := h.Exec(ctx, nil, "s6-setuidgid", "abc", "sh", "-eu", "-c", `[ -d "$1" ] && [ -r "$1" ] && [ -x "$1" ] && stat -Lc '%d:%i' -- "$1"`, "self-host-bazarr-path", root)
	if err != nil || result.ExitCode != 0 {
		return "", errors.New("media directory could not be inspected")
	}
	identity := strings.TrimSpace(string(result.Stdout))
	parts := strings.Split(identity, ":")
	if len(parts) != 2 {
		return "", errors.New("invalid directory identity")
	}
	if _, err := strconv.ParseUint(parts[0], 10, 64); err != nil {
		return "", errors.New("invalid directory device")
	}
	inode, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || inode == 0 {
		return "", errors.New("invalid directory inode")
	}
	return identity, nil
}

func (s libraryConnection) verifyConfigured(ctx context.Context, from, to *helpers.H, snapshot connectionSnapshot) error {
	roots, err := s.verifyTarget(ctx, from, to, snapshot)
	if err != nil {
		return err
	}
	for _, root := range roots {
		var directories []struct {
			Path     string `json:"path"`
			Children *bool  `json:"children"`
		}
		key := from.Secret(from.Key("api_key"))
		if err := readAPI(ctx, from, key, "/api/files/"+s.id+"?"+url.Values{"path": {path.Dir(root)}}.Encode(), &directories); err != nil {
			return err
		}
		found := false
		for _, directory := range directories {
			if strings.TrimSuffix(directory.Path, "/") == strings.TrimSuffix(root, "/") && directory.Children != nil && *directory.Children {
				found = true
			}
		}
		if !found {
			return s.manual(from, "Bazarr's configured target did not return the expected existing directory. An empty browser response is not proof of authorization; review credentials and paths.")
		}
	}
	observed, err := s.verifyLibrary(ctx, from, s.consumerReader(from, to))
	if err != nil {
		return err
	}
	if !slices.Equal(observed, roots) {
		return s.manual(from, "Bazarr's network context and the selected "+s.name+" disagree about the existing library roots; review the connection and mounts before trusting it.")
	}
	from.Info(s.name + " answered Bazarr's authenticated identity and library reads; enabled choices were preserved and no initial synchronization ran")
	return nil
}
