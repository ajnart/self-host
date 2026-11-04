package emby

import (
	"context"
	"errors"
	"net/http"

	"github.com/ajnart/self-host/internal/helpers"
)

func ReadServerLibraries(ctx context.Context, h *helpers.H, key, expectedID string) (string, map[string]string, error) {
	if key == "" {
		return "", nil, &helpers.ManualError{Hint: "Restore the existing Emby connection credential through its owner before retrying. No key was created or replaced."}
	}
	h.Log.Secret(key)
	public, err := h.HTTP(ctx, helpers.Request{Path: "/System/Info/Public", NoRedirect: true})
	if err != nil {
		return "", nil, err
	}
	var identity struct{ ID string }
	if public.Status != http.StatusOK || public.Decode(&identity) != nil || identity.ID == "" || expectedID == "" || identity.ID != expectedID {
		return "", nil, &helpers.ManualError{Hint: "Emby's current server identity does not match the expected server. Review ownership before sending the saved connection credential; no key was submitted or replaced."}
	}
	id, err := serverIdentity(ctx, h, key)
	if errors.Is(err, errAPIKeyRejected) {
		return "", nil, &helpers.ManualError{Hint: "Emby rejected the saved connection credential. Restore the intended credential through its owner before retrying; no key was replaced."}
	}
	if err != nil {
		return "", nil, err
	}
	if id != expectedID {
		return "", nil, &helpers.ManualError{Hint: "Emby's authenticated server identity changed during verification. Review the connection manually; no library selection or credential was changed."}
	}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/Library/MediaFolders", Header: embyHeader(key), NoRedirect: true})
	if err != nil {
		return "", nil, err
	}
	var data struct {
		Items []struct {
			ID, ServerID, Type, CollectionType string
		}
		TotalRecordCount *int
	}
	if resp.Status != http.StatusOK || resp.Decode(&data) != nil || data.Items == nil || data.TotalRecordCount == nil || *data.TotalRecordCount != len(data.Items) {
		return "", nil, errors.New("Emby did not return complete authenticated library folders; review its identity, credential and library inventory before retrying")
	}
	libraries := map[string]string{}
	seen := map[string]bool{}
	for _, item := range data.Items {
		if item.ID == "" || item.Type == "" || seen[item.ID] || item.ServerID != "" && item.ServerID != id {
			return "", nil, errors.New("Emby returned invalid library identities; review its libraries")
		}
		seen[item.ID] = true
		if item.Type != "CollectionFolder" {
			continue
		}
		switch item.CollectionType {
		case "music", "books", "musicvideos", "homevideos", "boxsets":
			continue
		case "movies":
			libraries[item.ID] = "movie"
		default:
			libraries[item.ID] = "show"
		}
	}
	return id, libraries, nil
}
