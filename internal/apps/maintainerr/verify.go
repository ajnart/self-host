package maintainerr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const serverHint = "Open Maintainerr at its current private Address and configure or repair your chosen Plex, Jellyfin or Emby media server under Services, then test its connection. Preserve the selected server, credentials, libraries, rules, collections and retention; do not run cleanup. Then retry self-host setup maintainerr."

const authenticationHint = "Maintainerr requires an existing authentication or media-server prerequisite that this read-only Setup cannot satisfy. Keep authentication and TLS verification enabled, repair the approved private connection without changing Access, then retry self-host setup maintainerr."

type serverIdentity struct {
	MachineID string `json:"machineId"`
	Version   string `json:"version"`
}

func verifyServer(ctx context.Context, h *helpers.H) error {
	var health struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := read(ctx, h, "/api/health/ready", &health); err != nil {
		return err
	}
	if health.Status != "ok" || health.Database != "ok" {
		return errors.New("Maintainerr did not confirm database readiness; read self-host logs maintainerr and retry self-host setup maintainerr")
	}
	var configured *bool
	if err := read(ctx, h, "/api/settings/test/setup", &configured); err != nil {
		return err
	}
	if configured == nil {
		return errors.New("Maintainerr did not report whether a media server is configured; retry self-host setup maintainerr")
	}
	if !*configured {
		return &helpers.ManualError{Hint: serverHint}
	}
	var backend struct {
		Type string `json:"type"`
	}
	if err := read(ctx, h, "/api/media-server/type", &backend); err != nil {
		return err
	}
	if backend.Type != "plex" && backend.Type != "jellyfin" && backend.Type != "emby" {
		return &helpers.ManualError{Hint: serverHint}
	}
	var identity serverIdentity
	if err := read(ctx, h, "/api/media-server", &identity); err != nil {
		return err
	}
	if strings.TrimSpace(identity.MachineID) == "" || strings.TrimSpace(identity.Version) == "" {
		return &helpers.ManualError{Hint: serverHint}
	}
	if backend.Type == "plex" {
		var connection struct {
			Status  string `json:"status"`
			Code    *int   `json:"code"`
			Message string `json:"message"`
		}
		if err := read(ctx, h, "/api/settings/test/plex", &connection); err != nil {
			return err
		}
		if connection.Status != "OK" || connection.Code == nil || *connection.Code != 1 || strings.TrimSpace(connection.Message) == "" {
			return &helpers.ManualError{Hint: serverHint}
		}
	} else {
		var users []struct {
			ID string `json:"id"`
		}
		if err := read(ctx, h, "/api/media-server/users", &users); err != nil {
			return err
		}
		if len(users) == 0 {
			return &helpers.ManualError{Hint: serverHint}
		}
		for _, user := range users {
			if strings.TrimSpace(user.ID) == "" {
				return &helpers.ManualError{Hint: serverHint}
			}
		}
	}
	var libraries []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := read(ctx, h, "/api/media-server/libraries", &libraries); err != nil {
		return err
	}
	if libraries == nil {
		return errors.New("Maintainerr did not return an explicit library list; retry self-host setup maintainerr")
	}
	for _, library := range libraries {
		if strings.TrimSpace(library.ID) == "" || library.Type != "movie" && library.Type != "show" {
			return errors.New("Maintainerr returned incomplete library identities; retry self-host setup maintainerr")
		}
	}
	var currentBackend struct {
		Type string `json:"type"`
	}
	var currentIdentity serverIdentity
	if err := read(ctx, h, "/api/media-server/type", &currentBackend); err != nil {
		return err
	}
	if err := read(ctx, h, "/api/media-server", &currentIdentity); err != nil {
		return err
	}
	if currentBackend.Type != backend.Type || currentIdentity.MachineID != identity.MachineID || strings.TrimSpace(currentIdentity.Version) == "" {
		return &helpers.ManualError{Hint: "Maintainerr's selected media-server identity changed during verification. Review that selection without replacing credentials or policies, then retry self-host setup maintainerr."}
	}
	return nil
}

func read(ctx context.Context, h *helpers.H, path string, value any) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Maintainerr's read-only API could not be reached; verify private connectivity and trusted TLS, then retry self-host setup maintainerr")
	}
	if response.Status == 401 || response.Status == 403 || response.Status >= 300 && response.Status < 400 {
		return &helpers.ManualError{Hint: authenticationHint}
	}
	if response.Status == 503 && path != "/api/health/ready" {
		return &helpers.ManualError{Hint: serverHint}
	}
	if response.Status != 200 {
		return fmt.Errorf("Maintainerr's read-only API returned HTTP %d on %s; read self-host logs maintainerr and retry self-host setup maintainerr", response.Status, path)
	}
	if response.Decode(value) != nil {
		return fmt.Errorf("Maintainerr returned an invalid response on %s; retry self-host setup maintainerr", path)
	}
	return nil
}
