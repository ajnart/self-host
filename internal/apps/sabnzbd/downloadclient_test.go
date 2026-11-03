package sabnzbd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
)

type sabnzbdReadPrerequisiteExecutor struct {
	helpers.Executor
	checked *bool
	missing bool
}

func (e *sabnzbdReadPrerequisiteExecutor) Exec(_ context.Context, _ string, _ map[string]string, args ...string) (system.Result, error) {
	if len(args) != 9 || strings.Join(args[:5], " ") != "s6-setuidgid abc sh -eu -c" || args[5] != downloadReadPrerequisiteProbe || args[6] != "self-host-sabnzbd-read-prerequisites" || args[7] != "/data/incomplete" || args[8] != "/data/complete" {
		return system.Result{}, errors.New("unexpected prerequisite command")
	}
	for _, required := range []string{`for directory in "$@"; do`, `test -d "$directory"`, `test -r "$directory"`, `test -w "$directory"`, `test -x "$directory"`} {
		if !strings.Contains(args[5], required) {
			return system.Result{}, errors.New("missing nonmutating directory check")
		}
	}
	if e.missing {
		return system.Result{ExitCode: 1}, nil
	}
	*e.checked = true
	return system.Result{Stdout: []byte("existing-download-folders\n")}, nil
}

func TestDownloadClientPreventsLazyReadMutations(t *testing.T) {
	for _, scenario := range []string{"working", "missing-executor", "missing-folder", "relative-incomplete", "relative-complete", "missing-default-category"} {
		t.Run(scenario, func(t *testing.T) {
			checked, defaultRead, queueCalls, mutations := false, false, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.Method != "POST" || r.URL.RawQuery != "" {
					t.Error("API credentials must remain in private POST forms")
				}
				mode, section, keyword := r.PostForm.Get("mode"), r.PostForm.Get("section"), r.PostForm.Get("keyword")
				switch mode {
				case "auth":
					json.NewEncoder(w).Encode(map[string]any{"auth": "apikey"})
				case "queue":
					queueCalls++
					if !checked || !defaultRead {
						mutations++
					}
					json.NewEncoder(w).Encode(map[string]any{"queue": map[string]any{"version": "5.1.3", "paused": false, "kbpersec": "0", "slots": []any{}}})
				case "history":
					json.NewEncoder(w).Encode(map[string]any{"history": map[string]any{"slots": []any{}}})
				case "get_config":
					switch section {
					case "misc":
						if keyword != "download_dir" && keyword != "complete_dir" {
							t.Errorf("unexpected global config read %q", keyword)
						}
						value := "/data/complete"
						if keyword == "download_dir" {
							value = "/data/incomplete"
						}
						if scenario == "relative-incomplete" && keyword == "download_dir" || scenario == "relative-complete" && keyword == "complete_dir" {
							value = "Downloads/relative"
						}
						json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"misc": map[string]any{keyword: value}}})
					case "categories":
						if keyword == "" && !defaultRead {
							mutations++
						}
						if keyword == "*" && scenario == "missing-default-category" {
							json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{}})
							return
						}
						if keyword == "*" {
							defaultRead = true
						}
						entries := []any{}
						for _, category := range []string{"*", "tv"} {
							if keyword == "" || keyword == category {
								entries = append(entries, map[string]any{"name": category, "dir": "tv", "script": "None"})
							}
						}
						json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"categories": entries}})
					case "sorters":
						json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"sorters": []any{}}})
					default:
						t.Errorf("unexpected config read %s", section)
					}
				case "fullstatus":
					mutations++
					json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"completedir": "/data/complete"}})
				default:
					t.Errorf("unexpected API operation %s", mode)
				}
			}))
			t.Cleanup(server.Close)
			h := porttest.New(t, porttest.Options{App: "sabnzbd", Service: "sabnzbd", Secrets: porttest.Secrets{"SABNZBD_API_KEY": "controlled-key"}}, server, &porttest.Recorder{})
			if scenario != "missing-executor" {
				h.Config.Exec = &sabnzbdReadPrerequisiteExecutor{checked: &checked, missing: scenario == "missing-folder"}
			}
			got, err := CompletedDownloadPath(context.Background(), h, "tv")
			if mutations != 0 {
				t.Fatalf("read-only Link triggered %d lazy writes", mutations)
			}
			if scenario == "working" {
				if err != nil || got != "/data/complete/tv" || !checked || queueCalls != 1 {
					t.Fatalf("working initialized path blocked or not verified: %q %v", got, err)
				}
			} else if !errors.Is(err, helpers.ErrManual) || queueCalls != 0 {
				t.Fatalf("unverified prerequisites reached lazy reads: queues=%d err=%v", queueCalls, err)
			}
		})
	}
}
