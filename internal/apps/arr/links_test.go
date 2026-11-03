package arr_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/qbittorrent"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/systemtest"
)

type appStatus struct {
	ID    string
	Setup stepstatus.Setup
	Steps []struct {
		ID     string
		Status stepstatus.Step
	}
}

func appsStatus(t *testing.T, out string) map[string]appStatus {
	t.Helper()
	var status struct{ Apps []appStatus }
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	byID := map[string]appStatus{}
	for _, a := range status.Apps {
		byID[a.ID] = a
	}
	return byID
}

func failedSteps(a appStatus) []string {
	var failed []string
	for _, s := range a.Steps {
		if s.Status == stepstatus.StepFail {
			failed = append(failed, s.ID)
		}
	}
	return failed
}

func sonarrQBittorrentCreationSchema(backend *arrtest.Arr) http.Handler {
	base := backend.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v3/downloadclient/schema" {
			if r.Header.Get("X-Api-Key") != backend.Key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{{
				"implementation": "QBittorrent", "implementationName": "qBittorrent",
				"configContract": "QBittorrentSettings", "protocol": "torrent", "priority": 1, "enable": false,
				"fields": []any{
					map[string]any{"name": "host", "value": "localhost"},
					map[string]any{"name": "port", "value": 8080},
					map[string]any{"name": "useSsl", "value": false},
					map[string]any{"name": "username", "value": nil},
					map[string]any{"name": "password", "value": nil},
					map[string]any{"name": "tvCategory", "value": "tv-sonarr"},
				},
			}})
			return
		}
		base.ServeHTTP(w, r)
	})
}

func TestAFailingLinkMakesItsConsumerIncompleteNotItsTarget(t *testing.T) {
	e := newEnv(t, arr.Sonarr(), qbittorrent.App{})
	e.Serve("sonarr", sonarrQBittorrentCreationSchema(e.Sonarr))
	accept := false
	var attempts atomic.Int64
	connect := e.Sonarr.AcceptDownloadClient
	e.Sonarr.AcceptDownloadClient = func(host string, port int, user, password string) bool {
		attempts.Add(1)
		return accept && connect(host, port, user, password)
	}

	r := e.Run("install", "sonarr", "qbittorrent", "--yes", "--json")

	if r.Code != 2 || r.StepEvent("sonarr", "link-qbittorrent") != "step.fail" {
		t.Fatalf("exit %d, the Link failure belongs to Sonarr\n%s", r.Code, r.Out)
	}
	if attempts.Load() != 1 || len(e.Sonarr.DownloadClients()) != 0 {
		t.Fatal("the Link must reach the rejected native creation test without leaving a resource")
	}
	if !strings.Contains(r.Out, "self-host setup sonarr") || strings.Contains(r.Out, "self-host setup qbittorrent") {
		t.Fatalf("the hint must name the consuming App only:\n%s", r.Out)
	}
	apps := appsStatus(t, e.Run("status", "--json").Out)
	if apps["sonarr"].Setup != stepstatus.SetupIncomplete || strings.Join(failedSteps(apps["sonarr"]), ",") != "link-qbittorrent" {
		t.Fatalf("sonarr should be incomplete because of its Link: %+v", apps["sonarr"])
	}
	if apps["qbittorrent"].Setup != stepstatus.SetupOK {
		t.Fatalf("qbittorrent's own Setup succeeded: %+v", apps["qbittorrent"])
	}

	accept = true
	retry := e.Run("setup", "qbittorrent", "--json")

	if retry.Code != 0 || attempts.Load() != 2 || len(e.Sonarr.DownloadClients()) != 1 {
		t.Fatalf("rerunning the target's Setup must retry the Link (exit %d)\n%s", retry.Code, retry.Out)
	}
	if apps := appsStatus(t, e.Run("status", "--json").Out); apps["sonarr"].Setup != stepstatus.SetupOK {
		t.Fatalf("sonarr should be ok once its Link works: %+v", apps["sonarr"])
	}
}

func TestRerunningTheConsumerRetriesItsFailedLink(t *testing.T) {
	e := newEnv(t, arr.Sonarr(), qbittorrent.App{})
	e.Serve("sonarr", sonarrQBittorrentCreationSchema(e.Sonarr))
	accept := false
	var attempts atomic.Int64
	connect := e.Sonarr.AcceptDownloadClient
	e.Sonarr.AcceptDownloadClient = func(host string, port int, user, password string) bool {
		attempts.Add(1)
		return accept && connect(host, port, user, password)
	}
	initial := e.Run("install", "qbittorrent", "sonarr", "--yes", "--json")
	if initial.Code != 2 || initial.StepEvent("sonarr", "link-qbittorrent") != "step.fail" || attempts.Load() != 1 || len(e.Sonarr.DownloadClients()) != 0 {
		t.Fatal("consumer retry must start with a rejected native creation test, not a schema hold")
	}

	accept = true
	r := e.Run("setup", "sonarr", "--json")

	if r.Code != 0 || r.StepEvent("sonarr", "link-qbittorrent") != "step.ok" || attempts.Load() != 2 || len(e.Sonarr.DownloadClients()) != 1 {
		t.Fatalf("exit %d\n%s", r.Code, r.Out)
	}
}

func TestALinkToAnUninstalledTargetIsNotScheduled(t *testing.T) {
	e := newEnv(t, arr.Sonarr(), qbittorrent.App{})

	r := e.Run("install", "sonarr", "--yes", "--json")

	step := r.Step("sonarr", "link-qbittorrent")
	if r.Code != 0 || step != nil {
		t.Fatalf("exit %d, an absent target must not schedule its Link\n%s", r.Code, r.Out)
	}
}

func TestAFailedUnlinkIsReportedAndKeptVisible(t *testing.T) {
	e := newEnv(t, arr.Sonarr(), qbittorrent.App{})
	schema := sonarrQBittorrentCreationSchema(e.Sonarr)
	var deletes atomic.Int64
	e.Serve("sonarr", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v3/downloadclient/") {
			deletes.Add(1)
		}
		schema.ServeHTTP(w, r)
	}))
	initial := e.Run("install", "sonarr", "qbittorrent", "--yes", "--json")
	if initial.Code != 0 || initial.StepEvent("sonarr", "link-qbittorrent") != "step.ok" || len(e.Sonarr.DownloadClients()) != 1 {
		t.Fatal("failed unlink must start with a successfully created owned resource")
	}
	before, err := json.Marshal(e.Sonarr.DownloadClients())
	if err != nil {
		t.Fatal(err)
	}
	var ownership string
	for line := range strings.SplitSeq(secrets(t, e), "\n") {
		if strings.HasPrefix(line, "SONARR_LINK_QBITTORRENT_RESOURCE=") {
			ownership = line
		}
	}
	if ownership == "" {
		t.Fatal("successful creation did not record its ownership")
	}
	e.Sonarr.FailDeletes = true

	r := e.Run("remove", "qbittorrent", "--yes", "--json")

	if r.Code != 2 || r.StepEvent("sonarr", "unlink-qbittorrent") != "step.fail" || deletes.Load() != 1 {
		t.Fatalf("exit %d, a failed unlink must surface\n%s", r.Code, r.Out)
	}
	after, err := json.Marshal(e.Sonarr.DownloadClients())
	if err != nil || string(after) != string(before) || !strings.Contains(secrets(t, e), ownership) {
		t.Fatal("failed owned deletion must retain the complete resource and ownership")
	}
	apps := appsStatus(t, e.Run("status", "--json").Out)
	if _, still := apps["qbittorrent"]; still {
		t.Fatal("qbittorrent should still be removed")
	}
	if strings.Join(failedSteps(apps["sonarr"]), ",") != "unlink-qbittorrent" {
		t.Fatalf("sonarr should keep the failed unlink visible: %+v", apps["sonarr"])
	}
}

func TestAUsersOwnQBittorrentClientIsLeftAlone(t *testing.T) {
	e := newEnv(t, arr.Sonarr(), qbittorrent.App{})
	schema := sonarrQBittorrentCreationSchema(e.Sonarr)
	var mutations atomic.Int64
	e.Serve("sonarr", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && strings.HasPrefix(r.URL.Path, "/api/v3/downloadclient") {
			mutations.Add(1)
		}
		schema.ServeHTTP(w, r)
	}))
	e.Sonarr.SeedDownloadClient(map[string]any{
		"name": "NAS qBittorrent", "implementation": "QBittorrent", "implementationName": "qBittorrent",
		"configContract": "QBittorrentSettings", "protocol": "torrent", "enable": true, "priority": 7, "tags": []int{4},
		"removeCompletedDownloads": false, "removeFailedDownloads": true,
		"fields": []any{
			map[string]any{"name": "host", "value": "nas.local"},
			map[string]any{"name": "port", "value": 8080},
			map[string]any{"name": "useSsl", "value": false},
			map[string]any{"name": "username", "value": "nas-user"},
			map[string]any{"name": "password", "value": "synthetic-nas-password"},
			map[string]any{"name": "tvCategory", "value": "personal-tv"},
		},
	})
	before, err := json.Marshal(e.Sonarr.DownloadClients())
	if err != nil {
		t.Fatal(err)
	}
	r := e.Run("install", "sonarr", "qbittorrent", "--yes", "--json")
	if r.Code != 3 || r.StepEvent("sonarr", "link-qbittorrent") != "step.manual" {
		t.Fatalf("personal enabled torrent client must hold fresh creation (exit %d)\n%s", r.Code, r.Out)
	}
	hint, _ := r.Step("sonarr", "link-qbittorrent")["hint"].(string)
	for _, want := range []string{"enabled competing client", "Settings > Download Clients", "configure the additional connection manually", "explicitly disable competing clients", "self-host setup sonarr", "does not change priorities, tags, indexer assignments or enabled flags"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("selection recovery lacks %q", want)
		}
	}
	apps := appsStatus(t, e.Run("status", "--json").Out)
	if apps["sonarr"].Setup != stepstatus.SetupManual || apps["qbittorrent"].Setup != stepstatus.SetupOK {
		t.Fatal("selection hold must remain isolated to the consuming App")
	}
	after, err := json.Marshal(e.Sonarr.DownloadClients())
	if err != nil || string(after) != string(before) || mutations.Load() != 0 || e.Sonarr.DownloadClientTests() != 0 || strings.Contains(secrets(t, e), "SONARR_LINK_QBITTORRENT_RESOURCE") {
		t.Fatal("selection hold changed the personal client or reached native/resource/ownership activity")
	}
	removed := e.Run("remove", "qbittorrent", "--yes", "--json")
	if removed.Code != 0 || mutations.Load() != 0 || e.Sonarr.DownloadClientTests() != 0 || strings.Contains(secrets(t, e), "SONARR_LINK_QBITTORRENT_RESOURCE") {
		t.Fatal("unowned target removal reached native/resource/ownership activity")
	}

	after, err = json.Marshal(e.Sonarr.DownloadClients())
	if err != nil || string(after) != string(before) || len(e.Sonarr.DownloadClients()) != 1 || e.Sonarr.DownloadClients()[0]["enable"] != true {
		t.Fatal("removal must preserve the complete genuinely enabled personal client")
	}
}

type secretMap map[string]string

func (s secretMap) Get(key string) string       { return s[key] }
func (s secretMap) Set(key, value string) error { s[key] = value; return nil }

func TestProwlarrReachesItsTargetAtTheTargetsContainerPort(t *testing.T) {
	prowlarr := arrtest.NewArr("prowlarr")
	server := httptest.NewServer(prowlarr.Handler())
	t.Cleanup(server.Close)
	secrets := secretMap{"PROWLARR_API_KEY": prowlarr.Key, "SONARR_API_KEY": "sonarr-key"}
	deps := systemtest.NewDeps(t)
	from := helpers.New(helpers.Config{
		App: "prowlarr", Service: "prowlarr", ContainerPort: 19696, BaseURL: server.URL, URL: server.URL,
		HTTP: server.Client(), Secrets: secrets, Log: events.Discard(), Now: deps.Now, Sleep: deps.Sleep,
	})
	to := helpers.New(helpers.Config{App: "sonarr", Service: "sonarr", ContainerPort: 18989, Port: 8989, Secrets: secrets, Log: events.Discard()})
	var link app.Link
	for _, l := range arr.Prowlarr().Links() {
		if l.Target == "sonarr" {
			link = l
		}
	}

	if err := link.Apply(context.Background(), from, to); err != nil {
		t.Fatalf("apply: %v", err)
	}

	applications := prowlarr.Applications()
	if len(applications) != 1 || arrtest.Field(applications[0], "baseUrl") != "http://sonarr:18989" || arrtest.Field(applications[0], "prowlarrUrl") != "http://prowlarr:19696" {
		t.Fatalf("applications %v, want Prowlarr at http://prowlarr:19696 syncing to http://sonarr:18989", applications)
	}
}
