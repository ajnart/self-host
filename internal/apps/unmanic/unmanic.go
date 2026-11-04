// Package unmanic is Unmanic. Traps: docs/catalog/traps/unmanic.md.
package unmanic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/firstadmin"
	"github.com/ajnart/self-host/internal/helpers"
)

// App is Unmanic. Traps: docs/catalog/traps/unmanic.md.
type App struct{ app.Defaults }

func (App) Info() app.Info {
	return app.Info{
		ID: "unmanic", Name: "Unmanic", Category: "Media",
		Description:   "Monitor and configure automated processing for your file libraries",
		Image:         "josh5/unmanic:latest",
		Docs:          "https://docs.unmanic.app/",
		Port:          8888,
		ContainerPort: 8888,
		Service:       "unmanic",
		WebSockets:    true,
	}
}

func (App) Compose(rc app.RenderContext) string {
	return `services:
  unmanic:
    image: josh5/unmanic:latest
    restart: unless-stopped
    environment:
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
    volumes:
      - ./config:/config
      - ./cache:/tmp/unmanic
      - ./library:/library:ro
    ports:
      - ${BIND_IP}:` + strconv.Itoa(rc.Port) + `:8888
`
}

const settingsPath = "config/.unmanic/config/settings.json"

func (App) Files(app.RenderContext, func(string) string) []app.File {
	return []app.File{{Path: settingsPath, Content: "{\"clear_pending_tasks_on_restart\":false}\n", Keep: true}}
}

const recoveryHint = "Read self-host logs unmanic. Unmanic must answer its read-only version, session, worker and pending-task APIs without a login redirect. Restore its saved config from a Backup if the installation identity changed, then retry self-host setup unmanic. Do not disable authentication or adopt a different installation."

var pendingBody = map[string]any{"start": 0, "length": 0, "search_value": "", "status": "all", "order_by": "priority", "order_direction": "desc", "library_ids": []int{}}

var probe = firstadmin.NoAccount{Probe: firstadmin.Probe{Path: "/unmanic/api/v2/version/read"}}

func (App) Setup(ctx context.Context, h *helpers.H) error {
	if err := probe.Setup(ctx, h); err != nil {
		return err
	}
	return h.Step(ctx, "monitor", "verify the Unmanic installation and read-only queue counts", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			statistics, err := ReadHomarrStats(ctx, h)
			return err == nil && h.Secret(h.Key("installation_uuid")) == statistics.InstallationUUID, err
		},
		Do: func(ctx context.Context) error {
			statistics, err := ReadHomarrStats(ctx, h)
			if err != nil {
				return err
			}
			if h.Secret(h.Key("installation_uuid")) != "" {
				return nil
			}
			return h.SetSecret(h.Key("installation_uuid"), statistics.InstallationUUID)
		},
		Hint: recoveryHint,
	})
}

func (App) Check(ctx context.Context, h *helpers.H) error {
	if err := probe.Check(ctx, h); err != nil {
		return err
	}
	_, err := ReadHomarrStats(ctx, h)
	return err
}

// Statistics are the identity and the three counts Homarr's Stats reads.
type Statistics struct {
	Version          string
	InstallationUUID string
	ActiveWorkers    int64
	TotalWorkers     int64
	PendingTasks     int64
}

func manual(format string, args ...any) error {
	return &helpers.ManualError{Hint: fmt.Sprintf(format, args...) + " " + recoveryHint}
}

func get(ctx context.Context, h *helpers.H, request helpers.Request, what string, into any) error {
	request.NoRedirect = true
	response, err := h.HTTP(ctx, request)
	if err != nil {
		return fmt.Errorf("Unmanic %s request failed; retry self-host setup unmanic", what)
	}
	switch {
	case response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || (response.Status >= 300 && response.Status < 400):
		return manual("Unmanic %s returned HTTP %d.", what, response.Status)
	case response.Status != http.StatusOK:
		return fmt.Errorf("Unmanic %s returned HTTP %d; retry self-host setup unmanic", what, response.Status)
	}
	if response.Decode(into) != nil {
		return manual("Unmanic %s did not return JSON.", what)
	}
	return nil
}

// ReadHomarrStats reads the installation identity and the worker and pending
// counts with read-only requests, and never persists or changes anything.
func ReadHomarrStats(ctx context.Context, h *helpers.H) (Statistics, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var statistics Statistics
	var version struct {
		Version string `json:"version"`
	}
	if err := get(ctx, h, helpers.Request{Path: "/unmanic/api/v2/version/read"}, "version", &version); err != nil {
		return statistics, err
	}
	if version.Version == "" {
		return statistics, errors.New("Unmanic returned no version; retry self-host setup unmanic")
	}
	var session struct {
		UUID string `json:"uuid"`
	}
	if err := get(ctx, h, helpers.Request{Path: "/unmanic/api/v2/session/state"}, "session state", &session); err != nil {
		return statistics, err
	}
	if session.UUID == "" {
		return statistics, errors.New("Unmanic has no installation identity yet; wait for it to finish starting and retry self-host setup unmanic")
	}
	if saved := h.Secret(h.Key("installation_uuid")); saved != "" && saved != session.UUID {
		return statistics, manual("The Unmanic installation identity changed.")
	}
	var workers struct {
		Workers []struct {
			Idle *bool `json:"idle"`
		} `json:"workers_status"`
	}
	if err := get(ctx, h, helpers.Request{Path: "/unmanic/api/v2/workers/status"}, "worker status", &workers); err != nil {
		return statistics, err
	}
	if workers.Workers == nil {
		return statistics, errors.New("Unmanic returned no worker status list; retry self-host setup unmanic")
	}
	for _, worker := range workers.Workers {
		if worker.Idle == nil {
			return statistics, errors.New("Unmanic returned incomplete worker status; retry self-host setup unmanic")
		}
		if !*worker.Idle {
			statistics.ActiveWorkers++
		}
	}
	var pending struct {
		RecordsTotal *int64 `json:"recordsTotal"`
	}
	if err := get(ctx, h, helpers.Request{Method: http.MethodPost, Path: "/unmanic/api/v2/pending/tasks", JSON: pendingBody}, "pending tasks", &pending); err != nil {
		return statistics, err
	}
	if pending.RecordsTotal == nil || *pending.RecordsTotal < 0 {
		return statistics, errors.New("Unmanic returned an invalid pending task count; retry self-host setup unmanic")
	}
	statistics.Version = version.Version
	statistics.InstallationUUID = session.UUID
	statistics.TotalWorkers = int64(len(workers.Workers))
	statistics.PendingTasks = *pending.RecordsTotal
	return statistics, nil
}
