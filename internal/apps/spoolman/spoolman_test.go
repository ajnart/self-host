package spoolman_test

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/spoolman"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
)

const spoolmanInfo = `{"version":"0.27.1","db_type":"DatabaseType.POSTGRES","debug_mode":false,"automatic_backups":false,"data_dir":"/home/app/.local/share/spoolman","logs_dir":"/home/app/.local/share/spoolman","backups_dir":"/home/app/.local/share/spoolman/backups"}`

type spoolmanSecrets map[string]string

func (s spoolmanSecrets) Get(key string) string       { return s[key] }
func (s spoolmanSecrets) Set(key, value string) error { s[key] = value; return nil }

type spoolmanResponse struct {
	status int
	body   string
}

func spoolmanServer(t *testing.T, overrides map[string]spoolmanResponse, requests *[]string) *httptest.Server {
	t.Helper()
	responses := map[string]spoolmanResponse{
		"/api/v1/health": {http.StatusOK, `{"status":"healthy"}`},
		"/api/v1/info":   {http.StatusOK, spoolmanInfo},
		"/api/v1/spool":  {http.StatusOK, `[]`},
	}
	maps.Copy(responses, overrides)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.RequestURI())
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("Spoolman verification must only read, without forwarding unrelated credentials")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		response, found := responses[r.URL.Path]
		if !found {
			t.Errorf("unexpected Spoolman endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if response.status >= 300 && response.status < 400 {
			w.Header().Set("Location", "/login?private-marker")
		}
		w.WriteHeader(response.status)
		w.Write([]byte(response.body))
	}))
	t.Cleanup(server.Close)
	return server
}

func spoolmanH(t *testing.T, server *httptest.Server, results *[]helpers.StepResult) *helpers.H {
	t.Helper()
	deps := systemtest.NewDeps(t)
	return helpers.New(helpers.Config{
		App: "spoolman", Service: "spoolman", Dir: t.TempDir(), BaseURL: server.URL, URL: server.URL,
		HTTP: server.Client(), Log: events.Discard(), Now: deps.Now, Sleep: deps.Sleep,
		Secrets: spoolmanSecrets{"ADMIN_PASSWORD": "unchanged-admin", "SPOOLMAN_DB_PASSWORD": "unchanged-database", "SPOOLMAN_API_KEY": "person-owned"},
		Record:  func(result helpers.StepResult) { *results = append(*results, result) },
	})
}

func TestSpoolmanDeploymentKeepsSupportedImageIdentityAndPrivatePersistentDatabase(t *testing.T) {
	a := spoolman.App{}
	info := a.Info()
	if info.ID != "spoolman" || info.Service != "spoolman" || info.Port != 7912 || info.ContainerPort != 8000 || !info.WebSockets {
		t.Fatalf("incorrect Spoolman metadata: %+v", info)
	}
	if info.Image != "ghcr.io/donkie/spoolman:latest" || len(a.Links()) != 0 {
		t.Fatal("Spoolman must use its official image without outbound Links")
	}
	inputs := a.Inputs()
	if len(inputs) != 1 || inputs[0].Key != "db_password" || !inputs[0].Private || !inputs[0].RecoveryOnly || inputs[0].Required || inputs[0].Default != "" {
		t.Fatal("Spoolman must expose only optional private recovery of the matching database password")
	}
	settings := map[string]string{"PUID": "0", "PGID": "0", "BIND_IP": "127.0.0.1"}
	before := maps.Clone(settings)
	definition := a.Compose(app.RenderContext{Port: 17912, Settings: settings})
	if !maps.Equal(settings, before) || definition != a.Compose(app.RenderContext{Port: 17912, Settings: map[string]string{"PUID": "1000", "PGID": "1000"}}) {
		t.Fatal("App-local deployment must not change or depend on Host user mapping")
	}
	type service struct {
		Image       string
		User        string
		Entrypoint  []string
		Command     []string
		Environment map[string]string
		Volumes     []string
		Ports       []string
		DependsOn   map[string]struct{ Condition string } `yaml:"depends_on"`
		Healthcheck struct{ Test []string }
	}
	var document struct{ Services map[string]service }
	if err := yaml.Unmarshal([]byte(definition), &document); err != nil {
		t.Fatal(err)
	}
	main, database := document.Services["spoolman"], document.Services["spoolman-db"]
	if len(document.Services) != 2 || main.Image != info.Image || database.Image != "docker.io/library/postgres:18-alpine" {
		t.Fatal("Spoolman must use its own officially supported PostgreSQL backend")
	}
	for name, item := range document.Services {
		if item.User != "" || len(item.Entrypoint) != 0 || len(item.Command) != 0 {
			t.Fatalf("%s must retain the official image's supported startup and privilege dropping", name)
		}
	}
	if _, found := main.Environment["PUID"]; found {
		t.Fatal("Spoolman's upstream entrypoint rejects the rootless Host PUID=0")
	}
	if _, found := main.Environment["PGID"]; found {
		t.Fatal("Spoolman must retain its App-local image identity")
	}
	wantEnvironment := map[string]string{
		"SPOOLMAN_DB_TYPE": "postgres", "SPOOLMAN_DB_HOST": "spoolman-db", "SPOOLMAN_DB_PORT": "5432",
		"SPOOLMAN_DB_NAME": "spoolman", "SPOOLMAN_DB_USERNAME": "spoolman", "SPOOLMAN_DB_PASSWORD": "${SPOOLMAN_DB_PASSWORD}",
		"EXTERNAL_DB_URL": "", "SPOOLMAN_UPDATE_CHECK": "FALSE", "SPOOLMAN_METRICS_ENABLED": "FALSE",
	}
	for key, want := range wantEnvironment {
		value, found := main.Environment[key]
		if !found || value != want {
			t.Errorf("Spoolman environment %s = %q, want %q", key, value, want)
		}
	}
	if database.Environment["POSTGRES_PASSWORD"] != "${SPOOLMAN_DB_PASSWORD}" || database.Environment["POSTGRES_USER"] != "spoolman" || database.Environment["POSTGRES_DB"] != "spoolman" {
		t.Fatal("database credentials and identity must be App-local and consistent")
	}
	if !slices.Equal(database.Volumes, []string{"./database:/var/lib/postgresql"}) || len(main.Volumes) != 0 || len(database.Ports) != 0 || !slices.Equal(main.Ports, []string{"${BIND_IP}:17912:8000"}) {
		t.Fatal("database must persist under the App folder without publishing its port or mounting user data into Spoolman")
	}
	if main.DependsOn["spoolman-db"].Condition != "service_healthy" || !slices.Equal(database.Healthcheck.Test, []string{"CMD", "pg_isready", "-h", "127.0.0.1", "-U", "spoolman", "-d", "spoolman"}) {
		t.Fatal("Spoolman must wait for its own database readiness")
	}
	if !reflect.DeepEqual(a.Secrets(), []app.SecretSpec{{Name: "db_password", Length: 48}}) {
		t.Fatal("only the guarded private database password may be declared; there is no Spoolman web account or API key")
	}
	for _, forbidden := range []string{"privileged:", "devices:", "docker.sock", "chown", "chmod", "AUTH_METHOD: trust"} {
		if strings.Contains(definition, forbidden) {
			t.Fatalf("unexpected deployment override %q", forbidden)
		}
	}
}

func TestSpoolmanAcceptsRealEmptyInventoryAndUnknownWeightsWithoutMutatingData(t *testing.T) {
	for _, inventory := range []string{
		`[]`,
		`[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0}]`,
		`[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0,"remaining_weight":null}]`,
		`[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0,"remaining_weight":0},{"id":2,"archived":true,"filament":{"id":2},"used_weight":12.5,"used_length":100,"remaining_weight":987.5}]`,
	} {
		t.Run(inventory, func(t *testing.T) {
			var requests []string
			server := spoolmanServer(t, map[string]spoolmanResponse{"/api/v1/spool": {200, inventory}}, &requests)
			var results []helpers.StepResult
			h := spoolmanH(t, server, &results)
			before := maps.Clone(h.Secrets.(spoolmanSecrets))
			data := filepath.Join(h.Dir, "spoolman.db")
			if err := os.WriteFile(data, []byte("existing-person-owned-inventory"), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				results = nil
				if err := (spoolman.App{}).Setup(t.Context(), h); err != nil {
					t.Fatal(err)
				}
				for _, result := range results {
					if result.Status != stepstatus.StepSkip {
						t.Fatalf("a valid existing inventory must not be reconfigured: %+v", result)
					}
				}
				if err := (spoolman.App{}).Check(t.Context(), h); err != nil {
					t.Fatal(err)
				}
			}
			if !maps.Equal(before, h.Secrets.(spoolmanSecrets)) {
				t.Fatal("verification changed existing credentials")
			}
			if got := clitest.Read(t, data); got != "existing-person-owned-inventory" {
				t.Fatal("verification touched existing inventory")
			}
			if len(requests) == 0 {
				t.Fatal("verification did not read the App")
			}
		})
	}
}

func TestSpoolmanRejectsMissingOrMalformedIdentityAndInventory(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"health body", "/api/v1/health", `{}`},
		{"unhealthy", "/api/v1/health", `{"status":"unhealthy"}`},
		{"info envelope", "/api/v1/info", `{"data":` + spoolmanInfo + `}`},
		{"version", "/api/v1/info", strings.Replace(spoolmanInfo, `"0.27.1"`, `"dev"`, 1)},
		{"backend", "/api/v1/info", strings.Replace(spoolmanInfo, `"DatabaseType.POSTGRES"`, `"unknown"`, 1)},
		{"info field", "/api/v1/info", strings.Replace(spoolmanInfo, `"debug_mode":false`, `"debug_mode":null`, 1)},
		{"null array", "/api/v1/spool", `null`},
		{"object", "/api/v1/spool", `{}`},
		{"HTML", "/api/v1/spool", `<html>private-marker</html>`},
		{"missing fields", "/api/v1/spool", `[{"id":1}]`},
		{"invalid ID", "/api/v1/spool", `[{"id":0.5,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0}]`},
		{"negative ID", "/api/v1/spool", `[{"id":-1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0}]`},
		{"missing filament", "/api/v1/spool", `[{"id":1,"archived":false,"used_weight":0,"used_length":0}]`},
		{"null archive", "/api/v1/spool", `[{"id":1,"archived":null,"filament":{"id":2},"used_weight":0,"used_length":0}]`},
		{"unknown consumption", "/api/v1/spool", `[{"id":1,"archived":false,"filament":{"id":2},"used_weight":null,"used_length":0}]`},
		{"negative weight", "/api/v1/spool", `[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0,"remaining_weight":-1}]`},
		{"nonfinite weight", "/api/v1/spool", `[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0,"remaining_weight":1e999}]`},
		{"duplicate ID", "/api/v1/spool", `[{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0},{"id":1,"archived":false,"filament":{"id":2},"used_weight":0,"used_length":0}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			server := spoolmanServer(t, map[string]spoolmanResponse{tc.path: {200, tc.body}}, &requests)
			var results []helpers.StepResult
			h := spoolmanH(t, server, &results)
			err := (spoolman.App{}).Check(t.Context(), h)
			if err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("invalid response must fail without echoing response data: %v", err)
			}
			if err := (spoolman.App{}).Setup(t.Context(), h); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("Setup must validate identity and inventory, not just readiness: %v", err)
			}
		})
	}
}

func TestSpoolmanVerifiesThePersistentBackendWithoutMigratingOtherSelections(t *testing.T) {
	for _, databaseType := range []string{"postgres", "DatabaseType.POSTGRES", "sqlite", "DatabaseType.SQLITE", "mysql", "cockroachdb"} {
		t.Run(databaseType, func(t *testing.T) {
			var requests []string
			info := strings.Replace(spoolmanInfo, "DatabaseType.POSTGRES", databaseType, 1)
			server := spoolmanServer(t, map[string]spoolmanResponse{"/api/v1/info": {200, info}}, &requests)
			var results []helpers.StepResult
			h := spoolmanH(t, server, &results)
			before := maps.Clone(h.Secrets.(spoolmanSecrets))
			err := (spoolman.App{}).Setup(t.Context(), h)
			if databaseType == "postgres" || databaseType == "DatabaseType.POSTGRES" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "Preserve the selected backend") {
					t.Fatalf("another backend must be preserved and reviewed, not accepted as this App's persistent database: %v", err)
				}
				if slices.Contains(requests, "GET /api/v1/spool") {
					t.Fatal("inventory must not be adopted from a different backend")
				}
			}
			if !maps.Equal(before, h.Secrets.(spoolmanSecrets)) {
				t.Fatal("backend review changed saved credentials")
			}
		})
	}
}

func TestSpoolmanPreservesAuthenticationAndRecordsActionableManualRecovery(t *testing.T) {
	for _, path := range []string{"/api/v1/health", "/api/v1/info", "/api/v1/spool"} {
		for _, code := range []int{301, 302, 303, 307, 308, 401, 403} {
			t.Run(path+"/"+http.StatusText(code), func(t *testing.T) {
				var requests []string
				server := spoolmanServer(t, map[string]spoolmanResponse{path: {code, "private-marker"}}, &requests)
				var results []helpers.StepResult
				h := spoolmanH(t, server, &results)
				before := maps.Clone(h.Secrets.(spoolmanSecrets))
				for range 2 {
					if err := (spoolman.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
						t.Fatalf("protected API must stay Manual: %v", err)
					}
					last := results[len(results)-1]
					if last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "authentication") || !strings.Contains(last.Hint, "self-host setup spoolman") || strings.Contains(last.Hint, "private-marker") {
						t.Fatalf("Manual recovery must be safe and actionable: %+v", last)
					}
				}
				if err := (spoolman.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("Check must retain the same protection boundary: %v", err)
				}
				if !maps.Equal(before, h.Secrets.(spoolmanSecrets)) {
					t.Fatal("authentication recovery changed credentials")
				}
			})
		}
	}
}

func TestSpoolmanUsesBoundedStartupReadiness(t *testing.T) {
	for _, code := range []int{500, 503} {
		var requests []string
		server := spoolmanServer(t, map[string]spoolmanResponse{"/api/v1/health": {code, "private-marker"}}, &requests)
		var results []helpers.StepResult
		h := spoolmanH(t, server, &results)
		if err := (spoolman.App{}).Setup(t.Context(), h); err == nil {
			t.Fatal("an unavailable App must not finish Setup")
		}
		if len(requests) < 2 || !strings.Contains(results[len(results)-1].Hint, "self-host logs spoolman") {
			t.Fatal("startup must wait with the existing bounded readiness helper and a logs hint")
		}
	}
}

func TestSpoolmanMissingDatabaseCredentialStopsWithoutGeneratingOrAdoptingAnything(t *testing.T) {
	var requests []string
	server := spoolmanServer(t, nil, &requests)
	var results []helpers.StepResult
	h := spoolmanH(t, server, &results)
	delete(h.Secrets.(spoolmanSecrets), "SPOOLMAN_DB_PASSWORD")
	before := maps.Clone(h.Secrets.(spoolmanSecrets))
	for range 2 {
		if err := (spoolman.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("a lost private database password must require recovery: %v", err)
		}
		last := results[len(results)-1]
		if last.Status != stepstatus.StepManual || last.ID != "database-credential" || !strings.Contains(last.Hint, "SPOOLMAN_DB_PASSWORD") {
			t.Fatalf("missing credential recovery was not retained: %+v", last)
		}
	}
	if err := (spoolman.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Check must retain lost-credential recovery: %v", err)
	}
	if len(requests) != 0 || !maps.Equal(before, h.Secrets.(spoolmanSecrets)) {
		t.Fatal("lost credential recovery generated a secret or adopted a reachable inventory")
	}
}

func TestSpoolmanFakeInstallAndRemovalKeepAppDatabaseAndManualCompose(t *testing.T) {
	var requests []string
	server := spoolmanServer(t, nil, &requests)
	deps := systemtest.NewDeps(t)
	cat := catalog.New([]app.App{spoolman.App{}}, nil)
	deps.In = strings.NewReader("synthetic-original-database-password\n")
	installed := clitest.Run(t, deps, cat, "install", "spoolman", "--yes", "--exposure", "local", "--set", "spoolman.port="+clitest.Port(server), "--set-file", "spoolman.db_password=-")
	if installed.Code != 0 {
		t.Fatal(installed.Out)
	}
	root := clitest.Root(t, deps)
	path := filepath.Join(root, "apps", "spoolman", "database", "18", "docker", "inventory-fixture")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("kept-inventory-history"), 0o600); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(root, "compose.yml")
	original := clitest.Read(t, composePath)
	edited := strings.Replace(original, "SPOOLMAN_METRICS_ENABLED:", "PERSON_OWNED_SETTING: kept\n      SPOOLMAN_METRICS_ENABLED:", 1)
	if edited == original {
		t.Fatal("fake install did not render the App definition")
	}
	if err := os.WriteFile(composePath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	setup := clitest.Run(t, deps, cat, "setup", "spoolman", "--json")
	if setup.Code != 0 || clitest.Read(t, composePath) != edited {
		t.Fatalf("Setup must preserve the person's Compose changes: %s", setup.Out)
	}
	removed := clitest.Run(t, deps, cat, "remove", "spoolman", "--yes")
	if removed.Code != 0 || clitest.Read(t, path) != "kept-inventory-history" {
		t.Fatalf("non-purge removal must keep App-local database files: %s", removed.Out)
	}
}

var _ app.App = spoolman.App{}
var _ app.SecretDeclarer = spoolman.App{}
var _ app.PreflightChecker = spoolman.App{}
var _ app.StandaloneGuide = spoolman.App{}
