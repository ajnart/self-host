package deluge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/deluge"
	"github.com/ajnart/self-host/internal/helpers"
)

type delugeArrFixture struct {
	web           *fakeDeluge
	hosts         [][]any
	methods       []string
	labels        map[string]map[string]any
	status        any
	errorMethod   string
	missingCookie bool
	allowSetup    bool
	calls         []string
}

func newDelugeArrFixture() *delugeArrFixture {
	return &delugeArrFixture{
		web: &fakeDeluge{password: "accepted-web-password", connected: true, defaultDaemon: "local-daemon", activeDaemon: "local-daemon",
			coreConfig: map[string]any{"download_location": "/data/torrents", "move_completed": false, "move_completed_path": "/data/torrents", "enabled_plugins": []string{"Label"}}},
		hosts:   [][]any{{"local-daemon", "127.0.0.1", 58846, "localclient"}},
		methods: []string{"label.get_labels", "label.get_options", "label.set_torrent"},
		labels: map[string]map[string]any{"tv": {"apply_move_completed": false, "move_completed": false, "move_completed_path": ""},
			"done": {"apply_move_completed": false, "move_completed": false, "move_completed_path": ""}},
	}
}

func (f *delugeArrFixture) handler(t *testing.T) http.Handler {
	base := f.web.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/" {
			base.ServeHTTP(w, r)
			return
		}
		var body struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int               `json:"id"`
		}
		if r.Method != "POST" || r.URL.Path != "/json" {
			t.Error("unexpected Deluge request")
			w.WriteHeader(400)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if json.Unmarshal(data, &body) != nil {
			t.Error("invalid Deluge request")
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(data)))
		f.web.mu.Lock()
		f.calls = append(f.calls, body.Method)
		cookie, _ := r.Cookie("_session_id")
		valid := cookie != nil && f.web.sessions[cookie.Value]
		var result any
		handled := true
		switch body.Method {
		case "system.listMethods":
			result = f.methods
		case "web.get_hosts":
			result = f.hosts
		case "web.get_host_status":
			var id string
			if len(body.Params) == 1 {
				json.Unmarshal(body.Params[0], &id)
			}
			state := "Online"
			if id == f.web.activeDaemon && f.web.connected {
				state = "Connected"
			}
			result = []any{id, state, "2.2.0"}
			if f.status != nil {
				result = f.status
			}
		case "label.get_labels":
			labels := []string{}
			for label := range f.labels {
				labels = append(labels, label)
			}
			result = labels
		case "label.get_options":
			var label string
			if len(body.Params) == 1 {
				json.Unmarshal(body.Params[0], &label)
			}
			result = f.labels[label]
		case "auth.login", "auth.check_session", "auth.delete_session", "web.connected", "web.get_config", "core.get_config", "web.update_ui":
			handled = false
		case "auth.change_password", "core.set_config", "web.connect", "web.set_config":
			if f.allowSetup {
				handled = false
			} else {
				t.Error("Deluge prerequisite attempted a target configuration change: " + body.Method)
			}
		default:
			t.Error("Deluge prerequisite attempted a mutation or unknown RPC: " + body.Method)
		}
		if body.Method == f.errorMethod || handled && !valid {
			f.web.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": body.ID, "error": map[string]any{"message": "private-rpc-response"}, "result": nil})
			return
		}
		f.web.mu.Unlock()
		if handled {
			json.NewEncoder(w).Encode(map[string]any{"id": body.ID, "error": nil, "result": result})
			return
		}
		if f.missingCookie && body.Method == "auth.login" {
			json.NewEncoder(w).Encode(map[string]any{"id": body.ID, "error": nil, "result": true})
			return
		}
		base.ServeHTTP(w, r)
	})
}

func delugeArrHelper(t *testing.T, f *delugeArrFixture) *helpers.H {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	var records []helpers.StepResult
	h := newH(t, server, &records)
	h.SetSecret("DELUGE_WEB_PASSWORD", "accepted-web-password")
	return h
}

func TestDelugeDownloadPathsUseWebSessionAndActualDestinations(t *testing.T) {
	f := newDelugeArrFixture()
	h := delugeArrHelper(t, f)
	f.web.coreConfig["move_completed"] = true
	f.web.coreConfig["move_completed_path"] = "/data/torrents/global"
	f.labels["tv"]["apply_move_completed"] = true
	f.labels["tv"]["move_completed"] = true
	f.labels["tv"]["move_completed_path"] = "/data/torrents/label"
	f.labels["done"]["apply_move_completed"] = true
	f.labels["done"]["move_completed"] = true
	f.labels["done"]["move_completed_path"] = "/data/torrents/imported"
	paths, err := deluge.DownloadPaths(context.Background(), h, "tv", "done", "/data/torrents/chosen", "/data/torrents/completed")
	if err != nil || !slices.Equal(paths, []string{"/data/torrents/chosen", "/data/torrents/label", "/data/torrents/completed", "/data/torrents/imported"}) {
		t.Fatalf("destinations=%v err=%v", paths, err)
	}
	if f.web.rotations != 0 || f.web.connects != 0 || f.web.configWrites != 0 || len(f.web.sessions) != 0 {
		t.Fatal("read-only prerequisite changed credentials/backend/config or retained its session")
	}
}

func TestDelugeDownloadPrerequisitesRemainManualAndNonmutating(t *testing.T) {
	for _, scenario := range []string{"password", "cookie", "disconnected", "remote", "default", "ambiguous", "host-status", "plugin", "methods", "label", "imported", "options", "core", "rpc", "rates", "category"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDelugeArrFixture()
			h := delugeArrHelper(t, f)
			category := "tv"
			switch scenario {
			case "password":
				h.SetSecret("DELUGE_WEB_PASSWORD", "rejected")
			case "cookie":
				f.missingCookie = true
			case "disconnected":
				f.web.connected = false
			case "remote":
				f.web.activeDaemon = "remote-daemon"
			case "default":
				f.web.defaultDaemon = "remote-daemon"
			case "ambiguous":
				f.hosts = append(f.hosts, []any{"duplicate", "localhost", 58846, "localclient"})
				f.web.defaultDaemon = ""
			case "host-status":
				f.status = []any{"different-id", "Connected", "2.2.0"}
			case "plugin":
				f.web.coreConfig["enabled_plugins"] = []string{}
			case "methods":
				f.methods = []string{"label.get_labels"}
			case "label":
				delete(f.labels, "tv")
			case "imported":
				delete(f.labels, "done")
			case "options":
				delete(f.labels["done"], "move_completed")
			case "core":
				delete(f.web.coreConfig, "download_location")
			case "rpc":
				f.errorMethod = "label.get_labels"
			case "rates":
				f.web.missingRates = true
			case "category":
				category = "Unsafe_Label"
			}
			_, err := deluge.DownloadPaths(context.Background(), h, category, "done", "", "")
			if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-rpc-response") {
				t.Fatalf("unsafe prerequisite accepted or disclosed: %v", err)
			}
			if f.web.rotations != 0 || f.web.connects != 0 || f.web.configWrites != 0 {
				t.Fatal("prerequisite repaired a person-owned choice")
			}
		})
	}
}

func TestDelugeDownloadPathsRetainGlobalAndLabelMoveSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name                              string
		globalMove, labelApply, labelMove bool
		download, completed               string
		paths                             []string
	}{
		{"global", true, false, false, "", "", []string{"/data/torrents", "/data/torrents/global"}},
		{"label", false, true, true, "", "", []string{"/data/torrents", "/data/torrents/label"}},
		{"label-disables-move", true, true, false, "", "", []string{"/data/torrents", "/data/torrents/global"}},
		{"stored-download", true, false, false, "/data/torrents/chosen", "", []string{"/data/torrents/chosen", "/data/torrents/global"}},
		{"stored-completed", true, false, false, "", "/data/torrents/completed", []string{"/data/torrents", "/data/torrents/completed"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newDelugeArrFixture()
			h := delugeArrHelper(t, f)
			f.web.coreConfig["move_completed"] = scenario.globalMove
			f.web.coreConfig["move_completed_path"] = "/data/torrents/global"
			f.labels["tv"]["apply_move_completed"] = scenario.labelApply
			f.labels["tv"]["move_completed"] = scenario.labelMove
			f.labels["tv"]["move_completed_path"] = "/data/torrents/label"
			paths, err := deluge.DownloadPaths(context.Background(), h, "tv", "", scenario.download, scenario.completed)
			if err != nil || !slices.Equal(paths, scenario.paths) {
				t.Fatal("wrong effective or reported destinations", paths, err)
			}
		})
	}
}

func TestDelugeSetupAndLinkKeepDistinctDaemonSelectionPolicies(t *testing.T) {
	for _, scenario := range []struct {
		name                    string
		hosts                   [][]any
		defaultDaemon           string
		setupManual, linkManual bool
	}{
		{"fresh", [][]any{{"local-daemon", "127.0.0.1", 58846, "localclient"}}, "", false, false},
		{"localhost", [][]any{{"local-daemon", "localhost", 58846, "localclient"}}, "local-daemon", false, false},
		{"ipv6", [][]any{{"local-daemon", "::1", 58846, "localclient"}}, "local-daemon", false, false},
		{"selected-default", [][]any{{"other", "127.0.0.1", 58846, "other-user"}, {"local-daemon", "127.0.0.1", 58846, "localclient"}}, "local-daemon", false, false},
		{"ambiguous", [][]any{{"other", "127.0.0.1", 58846, "other-user"}, {"local-daemon", "127.0.0.1", 58846, "localclient"}}, "", true, true},
		{"remote-default", [][]any{{"local-daemon", "127.0.0.1", 58846, "localclient"}}, "remote-daemon", true, true},
		{"unreadable-username", [][]any{{"local-daemon", "127.0.0.1", 58846, 17}}, "local-daemon", false, true},
		{"duplicate-remote-id", [][]any{{"local-daemon", "127.0.0.1", 58846, "localclient"}, {"remote", "other.example", 58846, "user"}, {"remote", "elsewhere.example", 58846, "user"}}, "local-daemon", false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newDelugeArrFixture()
			f.allowSetup = true
			f.hosts = scenario.hosts
			f.web.connected = false
			f.web.defaultDaemon = scenario.defaultDaemon
			f.web.password = "deluge"
			h := delugeArrHelper(t, f)
			err := (deluge.App{}).Setup(context.Background(), h)
			if scenario.setupManual {
				if !errors.Is(err, helpers.ErrManual) || f.web.connects != 0 || f.web.configWrites != 0 {
					t.Fatal("Setup changed an ambiguous or unsupported selection", err)
				}
			} else if err != nil || f.web.connects != 1 || f.web.defaultDaemon != "local-daemon" || f.web.rotations != 1 {
				t.Fatal("existing fresh Setup selection/credential behavior changed", err)
			}
			f.allowSetup = false
			connects, writes, rotations := f.web.connects, f.web.configWrites, f.web.rotations
			_, err = deluge.DownloadPaths(context.Background(), h, "tv", "", "", "")
			if scenario.linkManual {
				if !errors.Is(err, helpers.ErrManual) {
					t.Fatal("Link lost its stronger daemon identity checks", err)
				}
			} else if err != nil {
				t.Fatal("Link rejected the explicitly connected local daemon", err)
			}
			if f.web.connects != connects || f.web.configWrites != writes || f.web.rotations != rotations {
				t.Fatal("read-only Link inherited Setup mutation behavior")
			}
		})
	}
}
