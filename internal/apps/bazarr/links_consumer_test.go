package bazarr_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

type consumerExecutor struct {
	libraryExecutor
	targets map[string]*helpers.H
	rewrite func(target, route string, body []byte) []byte
	err     error
}

func (e consumerExecutor) ConsumerGET(ctx context.Context, _ string, target string, _ int, route, key string) (int, []byte, error) {
	if e.err != nil {
		return 0, nil, e.err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.targets[target].URL+route, nil)
	request.Header.Set("X-Api-Key", key)
	response, err := e.targets[target].Config.HTTP.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if e.rewrite != nil {
		body = e.rewrite(target, route, body)
	}
	return response.StatusCode, body, nil
}

func TestBazarrIsNativelyVerifiedOnlyWhenTheConsumerNetworkReadsTheSameLibrary(t *testing.T) {
	cases := map[string]struct {
		exec   func(map[string]*helpers.H) consumerExecutor
		manual bool
	}{
		"same library": {exec: func(targets map[string]*helpers.H) consumerExecutor { return consumerExecutor{targets: targets} }},
		"different roots": {manual: true, exec: func(targets map[string]*helpers.H) consumerExecutor {
			return consumerExecutor{targets: targets, rewrite: func(_, route string, body []byte) []byte {
				if strings.HasSuffix(route, "/rootfolder") {
					return []byte(`[{"id":1,"path":"/data/media/other","accessible":true}]`)
				}
				return body
			}}
		}},
		"not admitted": {manual: true, exec: func(targets map[string]*helpers.H) consumerExecutor {
			return consumerExecutor{targets: targets, err: errors.ErrUnsupported}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLibraryFixture(t)
			f.from.Config.Exec = tc.exec(f.targets)
			err := libraryLink(t, "sonarr").Apply(t.Context(), f.from, f.targets["sonarr"])
			if tc.manual && !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("expected Manual, got %v", err)
			}
			if !tc.manual && err != nil {
				t.Fatalf("expected native success, got %v", err)
			}
			if len(f.posts) != 1 || f.settings["general"].(map[string]any)["use_sonarr"] != false {
				t.Fatal("connection must be saved once with the enabled choice preserved")
			}
		})
	}
}
