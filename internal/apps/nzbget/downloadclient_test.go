package nzbget_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/apps/nzbget"
	"github.com/ajnart/self-host/internal/helpers"
)

func nzbgetDownloadSettings() []map[string]string {
	return []map[string]string{
		{"Name": "ControlUsername", "Value": "admin"},
		{"Name": "ControlPassword", "Value": password},
		{"Name": "DestDir", "Value": "/data/usenet/complete"},
		{"Name": "AppendCategoryDir", "Value": "yes"},
		{"Name": "KeepHistory", "Value": "30"},
		{"Name": "Extensions", "Value": ""},
		{"Name": "Category1.Name", "Value": "tv"},
		{"Name": "Category2.Name", "Value": "Movies"},
		{"Name": "Category3.Name", "Value": "Music"},
		{"Name": "Server1.Password", "Value": "provider-secret-must-stay-private"},
	}
}

func nzbgetDownloadSet(entries []map[string]string, name, value string) []map[string]string {
	for _, entry := range entries {
		if entry["Name"] == name {
			entry["Value"] = value
			return entries
		}
	}
	return append(entries, map[string]string{"Name": name, "Value": value})
}

func nzbgetDownloadTarget(t *testing.T, entries []map[string]string) (*helpers.H, *sync.Map) {
	t.Helper()
	settings := &sync.Map{}
	settings.Store("config", entries)
	base := handler(settings)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("could not read RPC fixture request")
			w.WriteHeader(400)
			return
		}
		var request struct{ Method string }
		if r.Method != "POST" || r.URL.Path != "/jsonrpc" || len(r.URL.RawQuery) != 0 || json.Unmarshal(body, &request) != nil || request.Method != "version" && request.Method != "config" {
			t.Error("download-client helper attempted a mutation or an unrelated provider/queue action")
			w.WriteHeader(400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		base.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	var results []helpers.StepResult
	return newH(t, server, &results), settings
}

func TestNZBGetCompletedDownloadPathUsesActiveCategoryWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name, category, setting, value, want string
	}{
		{name: "tv", category: "tv", want: "/data/usenet/complete/tv"},
		{name: "movie exact case", category: "Movies", want: "/data/usenet/complete/Movies"},
		{name: "music exact case", category: "Music", want: "/data/usenet/complete/Music"},
		{name: "append disabled", category: "tv", setting: "AppendCategoryDir", value: "no", want: "/data/usenet/complete"},
		{name: "category override", category: "tv", setting: "Category1.DestDir", value: "/downloads/chosen/", want: "/downloads/chosen"},
		{name: "legacy folders", category: "tv", setting: "DestDir", value: "/downloads/completed", want: "/downloads/completed/tv"},
		{name: "active resolved path", category: "tv", setting: "DestDir", value: "/chosen parent/complete/", want: "/chosen parent/complete/tv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := nzbgetDownloadSettings()
			if tc.setting != "" {
				entries = nzbgetDownloadSet(entries, tc.setting, tc.value)
			}
			h, settings := nzbgetDownloadTarget(t, entries)
			before := h.Secret("ADMIN_PASSWORD")
			completed, err := nzbget.CompletedDownloadPath(context.Background(), h, tc.category)
			if err != nil || completed != tc.want {
				t.Fatalf("completed=%q error=%v", completed, err)
			}
			active, _ := settings.Load("config")
			if !reflect.DeepEqual(active, entries) || h.Secret("ADMIN_PASSWORD") != before {
				t.Fatal("read-only resolution changed configuration or credentials")
			}
		})
	}
}

func TestNZBGetCompletedDownloadPathKeepsUnsafeChoicesManual(t *testing.T) {
	for _, tc := range []struct{ name, category, setting, value string }{
		{name: "missing category", category: "missing"},
		{name: "case mismatch", category: "movies"},
		{name: "empty category", category: ""},
		{name: "traversal category", category: "../tv"},
		{name: "wildcard category", category: "*"},
		{name: "disabled history", category: "tv", setting: "KeepHistory", value: "0"},
		{name: "excess history", category: "tv", setting: "KeepHistory", value: "25001"},
		{name: "negative history", category: "tv", setting: "KeepHistory", value: "-1"},
		{name: "unknown append", category: "tv", setting: "AppendCategoryDir", value: "maybe"},
		{name: "global extension", category: "tv", setting: "Extensions", value: "Move.py"},
		{name: "category extension", category: "tv", setting: "Category1.Extensions", value: "VideoSort.py"},
		{name: "legacy category script", category: "tv", setting: "Category1.DefScript", value: "Move.py"},
		{name: "relative global", category: "tv", setting: "DestDir", value: "complete"},
		{name: "unresolved global", category: "tv", setting: "DestDir", value: "${MainDir}/complete"},
		{name: "root destination", category: "tv", setting: "DestDir", value: "/"},
		{name: "relative override", category: "tv", setting: "Category1.DestDir", value: "../outside"},
		{name: "unclean override", category: "tv", setting: "Category1.DestDir", value: "/downloads/../outside"},
		{name: "control override", category: "tv", setting: "Category1.DestDir", value: "/downloads/\nprivate"},
		{name: "empty preceding category", category: "Music", setting: "Category1.Name", value: ""},
		{name: "ambiguous category", category: "tv", setting: "Category2.Name", value: "TV"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := nzbgetDownloadSettings()
			if tc.setting != "" {
				entries = nzbgetDownloadSet(entries, tc.setting, tc.value)
			}
			h, _ := nzbgetDownloadTarget(t, entries)
			if _, err := nzbget.CompletedDownloadPath(context.Background(), h, tc.category); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unsafe configuration must remain Manual: %v", err)
			}
		})
	}
}

func TestNZBGetCompletedDownloadPathRejectsAuthenticationAndMalformedRPC(t *testing.T) {
	for _, scenario := range []string{"rejected login", "missing login", "wrong version", "null config", "duplicate option", "null option", "category gap", "masked control login", "different control login"} {
		t.Run(scenario, func(t *testing.T) {
			entries := nzbgetDownloadSettings()
			if scenario == "category gap" {
				entries = append(entries[:6], entries[7:]...)
			}
			h, settings := nzbgetDownloadTarget(t, entries)
			switch scenario {
			case "rejected login":
				h.Secrets.Set("ADMIN_PASSWORD", "rejected-private-password")
			case "missing login":
				h.Secrets.Set("ADMIN_PASSWORD", "")
			case "wrong version":
				settings.Store("version", "unsupported-private-response")
			case "null config":
				settings.Store("config", nil)
			case "duplicate option":
				settings.Store("config", append(entries, map[string]string{"Name": "KeepHistory", "Value": "10"}))
			case "null option":
				settings.Store("config", []map[string]any{{"Name": "DestDir", "Value": nil}})
			case "masked control login":
				settings.Store("config", nzbgetDownloadSet(entries, "ControlPassword", "***"))
			case "different control login":
				settings.Store("config", nzbgetDownloadSet(entries, "ControlUsername", "restricted"))
			}
			_, err := nzbget.CompletedDownloadPath(context.Background(), h, "Music")
			if err == nil || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "provider-secret") {
				t.Fatalf("invalid RPC was accepted or disclosed: %v", err)
			}
		})
	}
}
