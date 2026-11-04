package emby_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/apps/emby"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestReadServerLibrariesReusesExistingCredentialWithoutHomarrOrAuthentication(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate", "wrong-server", "missing-count", "public-mismatch", "public-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var authenticatedRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/System/Info/Public" {
					if r.Header.Get("X-Emby-Token") != "" {
						t.Error("public identity received a private credential")
					}
					if mode == "public-unavailable" {
						w.WriteHeader(401)
						return
					}
					if mode == "public-mismatch" {
						w.Write([]byte(`{"Id":"other-server"}`))
						return
					}
					w.Write([]byte(`{"Id":"emby-id"}`))
					return
				}
				authenticatedRequests.Add(1)
				if r.Method != "GET" || r.Header.Get("X-Emby-Token") != "existing-key" {
					t.Error("read-only action did not reuse its existing credential")
				}
				switch r.URL.Path {
				case "/emby/System/Info":
					w.Write([]byte(`{"Id":"emby-id","Version":"4.10.2.0"}`))
				case "/Library/MediaFolders":
					items := []map[string]string{{"Id": "movie", "Type": "CollectionFolder", "CollectionType": "movies", "ServerId": "emby-id"}, {"Id": "music", "Type": "CollectionFolder", "CollectionType": "music"}}
					if mode == "duplicate" {
						items = append(items, items[0])
					}
					if mode == "wrong-server" {
						items[0]["ServerId"] = "other-server"
					}
					data := map[string]any{"Items": items, "TotalRecordCount": len(items)}
					if mode == "missing-count" {
						delete(data, "TotalRecordCount")
					}
					json.NewEncoder(w).Encode(data)
				default:
					t.Errorf("read-only action exceeded local identity/library scope: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			h := helpers.New(helpers.Config{App: "emby", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard()})
			id, libraries, err := emby.ReadServerLibraries(context.Background(), h, "existing-key", "emby-id")
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid authenticated inventory accepted")
				}
				if strings.HasPrefix(mode, "public-") && authenticatedRequests.Load() != 0 {
					t.Fatal("unverified target received the saved credential")
				}
				return
			}
			if err != nil || id != "emby-id" || !reflect.DeepEqual(libraries, map[string]string{"movie": "movie"}) {
				t.Fatal(id, libraries, err)
			}
		})
	}
}

func TestReadServerLibrariesRecoveryHintsAreConsumerNeutral(t *testing.T) {
	for _, mode := range []string{"missing-key", "public-identity", "rejected-key", "authenticated-identity", "inventory"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Info/Public":
					id := "emby-id"
					if mode == "public-identity" {
						id = "different-id"
					}
					json.NewEncoder(w).Encode(map[string]string{"Id": id})
				case "/emby/System/Info":
					if mode == "rejected-key" {
						w.WriteHeader(403)
						return
					}
					id := "emby-id"
					if mode == "authenticated-identity" {
						id = "different-id"
					}
					json.NewEncoder(w).Encode(map[string]string{"Id": id, "Version": "4.10.2.0"})
				case "/Library/MediaFolders":
					w.Write([]byte(`{"Items":[]}`))
				default:
					t.Errorf("unexpected read-only recovery request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			h := helpers.New(helpers.Config{App: "emby", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard()})
			key := "existing-key"
			if mode == "missing-key" {
				key = ""
			}
			_, _, err := emby.ReadServerLibraries(context.Background(), h, key, "emby-id")
			if err == nil || strings.Contains(strings.ToLower(err.Error()), "seerr") {
				t.Fatalf("target-owned recovery is missing or names a consumer: %v", err)
			}
		})
	}
}
