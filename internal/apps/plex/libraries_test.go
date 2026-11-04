package plex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/plex"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestReadServerLibrariesUsesVerifiedLocalTokenAndFiltersUnsupportedLibraries(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("read-only action mutated the target")
			}
			switch r.URL.Path {
			case "/identity":
				json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"machineIdentifier": machineID, "claimed": true}})
			case "/library/sections":
				if r.Header.Get("X-Plex-Token") != serverToken {
					w.WriteHeader(401)
					return
				}
				libraries := []map[string]string{{"key": "movie", "type": "movie", "agent": "movies"}, {"key": "personal", "type": "movie", "agent": "com.plexapp.agents.none"}, {"key": "music", "type": "artist", "agent": "music"}}
				if duplicate {
					libraries = append(libraries, libraries[0])
				}
				json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"size": len(libraries), "Directory": libraries}})
			default:
				t.Errorf("read-only library action exceeded local identity/library scope: %s", r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		t.Cleanup(server.Close)
		var records []helpers.StepResult
		h := newH(t, server, &records, map[string]string{"verified_server_id": machineID})
		h.SetSecret("PLEX_API_KEY", serverToken)
		h.SetSecret("PLEX_SERVER_ID", machineID)
		id, libraries, err := plex.ReadServerLibraries(context.Background(), h)
		if duplicate {
			if err == nil {
				t.Fatal("duplicate native identifiers accepted")
			}
			continue
		}
		if err != nil || id != machineID || !reflect.DeepEqual(libraries, map[string]string{"movie": "movie"}) {
			t.Fatal(id, libraries, err)
		}
	}
}

func TestReadServerLibrariesOwnershipHintIsConsumerNeutral(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unverified ownership made a target request")
		w.WriteHeader(403)
	}))
	t.Cleanup(server.Close)
	var records []helpers.StepResult
	h := newH(t, server, &records, nil)
	_, _, err := plex.ReadServerLibraries(context.Background(), h)
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "seerr") {
		t.Fatalf("target-owned ownership recovery names a consumer: %v", err)
	}
}

func TestReadServerLibrariesKeepsSharedCardinalityValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity":
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": map[string]any{"machineIdentifier": machineID, "claimed": true}})
		case "/library/sections":
			if r.Header.Get("X-Plex-Token") == "" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"MediaContainer":{"size":1,"Directory":[]}}`))
		default:
			t.Errorf("unexpected target request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	var records []helpers.StepResult
	h := newH(t, server, &records, map[string]string{"verified_server_id": machineID})
	h.SetSecret("PLEX_API_KEY", serverToken)
	h.SetSecret("PLEX_SERVER_ID", machineID)
	_, _, err := plex.ReadServerLibraries(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "incomplete library sections") {
		t.Fatalf("shared cardinality validation was lost: %v", err)
	}
}
