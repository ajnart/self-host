package seerr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/ajnart/self-host/internal/helpers"
)

type serverOwnership struct {
	ID             *int64 `json:"id"`
	Target         string `json:"target"`
	Implementation string `json:"implementation"`
	Hostname       string `json:"hostname"`
	Port           int    `json:"port"`
	KeyHash        string `json:"keyHash"`
	Pending        bool   `json:"pending"`
}

func (s server) ownershipKey(h *helpers.H) string {
	return h.Key("owned_" + s.id)
}

func (s server) manualOwnership(h *helpers.H) error {
	h.Info("Seerr's " + s.name + " entry has missing, uncertain or changed ownership. Review /settings/services manually before recovery; endpoint matches alone never permit adoption or deletion, and uncertain mutations are not retried automatically.")
	return helpers.ErrManual
}

func decodeServerResponse(resp helpers.Response, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(resp.Body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("Seerr returned invalid server data")
	}
	return nil
}

func serverResourceID(item map[string]any) (int64, bool) {
	id, ok := item["id"].(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := id.Int64()
	return parsed, err == nil && parsed >= 0
}

func (s server) validOwned(item map[string]any, record serverOwnership, to *helpers.H) bool {
	id, valid := serverResourceID(item)
	key, keyValid := item["apiKey"].(string)
	baseURL, baseValid := item["baseUrl"].(string)
	if !valid || record.ID == nil || id != *record.ID || !s.points(item, to) || !keyValid || key == "" || helpers.Fingerprint(key) != record.KeyHash || !baseValid || baseURL != "" || item["useSsl"] != false || item["is4k"] != false {
		return false
	}
	if s.id == "sonarr" {
		_, valid := item["enableSeasonFolders"].(bool)
		return valid
	}
	availability, valid := item["minimumAvailability"].(string)
	return s.id == "radarr" && valid && availability != ""
}

func (s server) saveOwnership(h *helpers.H, record serverOwnership) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return h.SetSecret(s.ownershipKey(h), string(raw))
}

func (s server) findOwned(ctx context.Context, from, to *helpers.H) ([]string, error) {
	if from.App != "seerr" || to.App != s.id || to.Service == "" || to.ContainerPort <= 0 {
		return nil, s.manualOwnership(from)
	}
	var record serverOwnership
	raw := from.Secret(s.ownershipKey(from))
	if raw != "" {
		if json.Unmarshal([]byte(raw), &record) != nil || record.Pending || record.ID == nil || *record.ID < 0 || record.Target != to.App || record.Implementation != s.id || record.Hostname != to.Service || record.Port != to.ContainerPort || record.KeyHash == "" {
			return nil, s.manualOwnership(from)
		}
	}
	items, err := s.list(ctx, from)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, item := range items {
		id, valid := serverResourceID(item)
		if raw != "" && valid && id == *record.ID {
			if !s.validOwned(item, record, to) || len(ids) != 0 {
				return nil, s.manualOwnership(from)
			}
			ids = append(ids, strconv.FormatInt(id, 10))
			continue
		}
		if s.points(item, to) {
			return nil, s.manualOwnership(from)
		}
	}
	if raw != "" && len(ids) == 0 {
		return nil, s.manualOwnership(from)
	}
	return ids, nil
}

func (s server) createOwned(ctx context.Context, from, to *helpers.H, body map[string]any) error {
	record := serverOwnership{
		Target: to.App, Implementation: s.id, Hostname: to.Service, Port: to.ContainerPort,
		KeyHash: helpers.Fingerprint(to.Secret(to.Key("api_key"))), Pending: true,
	}
	if err := s.saveOwnership(from, record); err != nil {
		return err
	}
	resp, err := from.HTTP(ctx, helpers.Request{Method: "POST", Path: "/api/v1/settings/" + s.id, Header: keyHeader(from), JSON: body})
	if err != nil {
		from.Info("The " + s.name + " creation request could not be completed reliably. Its outcome needs manual review; the ownership hold was kept.")
		return s.manualOwnership(from)
	}
	if resp.Status != 201 {
		from.Info("Seerr returned HTTP " + strconv.Itoa(resp.Status) + " while creating " + s.name + "; the ownership hold was kept for manual review.")
		return s.manualOwnership(from)
	}
	var created map[string]any
	if decodeServerResponse(resp, &created) != nil {
		return s.manualOwnership(from)
	}
	id, valid := serverResourceID(created)
	record.ID = &id
	if !valid || !s.validOwned(created, record, to) || created["preventSearch"] != body["preventSearch"] || created["isDefault"] != body["isDefault"] {
		return s.manualOwnership(from)
	}
	record.Pending = false
	return s.saveOwnership(from, record)
}

func (s server) deleteOwned(ctx context.Context, from, to *helpers.H, id string) error {
	ids, err := s.findOwned(ctx, from, to)
	if err != nil {
		return err
	}
	if len(ids) != 1 || ids[0] != id {
		return s.manualOwnership(from)
	}
	var record serverOwnership
	if json.Unmarshal([]byte(from.Secret(s.ownershipKey(from))), &record) != nil {
		return s.manualOwnership(from)
	}
	record.Pending = true
	if err := s.saveOwnership(from, record); err != nil {
		return err
	}
	resp, err := from.HTTP(ctx, helpers.Request{Method: "DELETE", Path: "/api/v1/settings/" + s.id + "/" + id, Header: keyHeader(from)})
	if err != nil {
		from.Info("The " + s.name + " removal request could not be completed reliably. Its outcome needs manual review; the ownership hold was kept.")
		return s.manualOwnership(from)
	}
	if !resp.OK() {
		from.Info("Seerr returned HTTP " + strconv.Itoa(resp.Status) + " while removing " + s.name + "; the ownership hold was kept for manual review.")
		return s.manualOwnership(from)
	}
	return from.SetSecret(s.ownershipKey(from), "")
}
