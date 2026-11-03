package servarr

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

func DownloadClientLinks(api, categoryField, category string) []app.Link {
	return []app.Link{
		QBittorrentLink(api, categoryField, category),
		SABnzbdLink(api, categoryField, category),
		NZBGetLink(api, categoryField, category),
		DelugeLink(api, categoryField, category),
		TransmissionLink(api, categoryField, category),
	}
}

type downloadProtocolFamily uint8

const (
	downloadProtocolSingle downloadProtocolFamily = iota
	downloadProtocolUsenet
	downloadProtocolTorrent
)

func (p providerLink) admitsDownloadProtocol(from *helpers.H, value any) bool {
	protocol, ok := value.(string)
	if !ok {
		return false
	}
	if p.protocolFamily == downloadProtocolSingle {
		return protocol == p.protocol
	}
	expected := map[string]string{"sonarr": "tvCategory", "radarr": "movieCategory", "lidarr": "musicCategory"}[from.App]
	if expected == "" || expected != p.categoryField || from.App == "lidarr" && p.api != V1 || from.App != "lidarr" && p.api != V3 {
		return false
	}
	switch p.protocolFamily {
	case downloadProtocolUsenet:
		return protocol == "usenet" || from.App == "lidarr" && protocol == "UsenetDownloadProtocol"
	case downloadProtocolTorrent:
		return protocol == "torrent" || from.App == "lidarr" && protocol == "TorrentDownloadProtocol"
	default:
		return false
	}
}

func (p providerLink) downloadList(ctx context.Context, h *helpers.H, path string) ([]provider, error) {
	if h.Secret(h.Key("api_key")) == "" {
		return nil, p.manual(h, "the consumer's accepted API key is missing; restore consumer API access before retrying")
	}
	items, err := p.list(ctx, h, path)
	if err != nil {
		return nil, p.manual(h, "the authenticated download-client schema or resources could not be verified; restore consumer API access and retry without changing ownership")
	}
	return items, nil
}

func (p providerLink) downloadTemplate(ctx context.Context, h *helpers.H) (provider, error) {
	templates, err := p.downloadList(ctx, h, p.path+"/schema")
	if err != nil {
		return nil, err
	}
	return p.selectDownloadTemplate(h, templates)
}

func (p providerLink) selectDownloadTemplate(h *helpers.H, templates []provider) (provider, error) {
	identity := p
	legacyCreation := p.path == "/downloadclient" && p.target == "qbittorrent" && p.implementation == "QBittorrent" && p.connectionFields == nil
	if legacyCreation {
		identity.configContract = "QBittorrentSettings"
		identity.protocolFamily = downloadProtocolTorrent
	}
	var selected provider
	for _, item := range templates {
		if item["implementation"] != p.implementation {
			continue
		}
		if selected != nil || item["configContract"] != identity.configContract || !identity.admitsDownloadProtocol(h, item["protocol"]) {
			return nil, p.manual(h, "the download-client schema is ambiguous or unsupported")
		}
		selected = item
	}
	if selected == nil {
		return nil, p.manual(h, "the expected download-client schema is unavailable")
	}
	required := []connectionField{{p.endpointField, ""}, {p.categoryField, ""}}
	if legacyCreation {
		required = append(required, connectionField{"port", float64(0)}, connectionField{"useSsl", false}, connectionField{"username", ""}, connectionField{"password", ""})
	}
	if p.protocolFamily != downloadProtocolSingle {
		required = append(required, connectionField{"port", float64(0)}, connectionField{"useSsl", false}, connectionField{"urlBase", ""}, connectionField{"password", ""})
		if p.implementation == "Sabnzbd" {
			required = append(required, connectionField{"apiKey", ""})
		}
		if p.implementation == "Sabnzbd" || p.implementation == "Nzbget" || p.implementation == "Transmission" {
			required = append(required, connectionField{"username", ""})
		}
	}
	for _, field := range required {
		value := selected.field(field.Name)
		if err := selected.setField(field.Name, value); err != nil {
			return nil, p.manual(h, "a required download-client schema field is unavailable")
		}
		if value == nil && field.Value == "" {
			continue
		}
		if reflect.TypeOf(value) != reflect.TypeOf(field.Value) {
			return nil, p.manual(h, "a required download-client schema field has an unsupported type")
		}
	}
	if p.protocolFamily != downloadProtocolSingle {
		for _, raw := range selected["fields"].([]any) {
			field := raw.(map[string]any)
			switch field["value"].(type) {
			case nil, string, bool, float64:
			default:
				return nil, p.manual(h, "a download-client schema field has an unsupported type")
			}
			switch field["name"] {
			case "apiKey", "username", "password":
				if value := field["value"]; value != nil {
					if _, ok := value.(string); !ok {
						return nil, p.manual(h, "a download-client credential schema field has an unsupported type")
					}
				}
			}
		}
	}
	return selected, nil
}

func (p providerLink) admitDownloadCreation(from *helpers.H, template provider, items, schemas []provider) error {
	refusal := &helpers.ManualError{Hint: "Review " + from.App + " > Settings > Download Clients: an enabled competing client or unreadable selection contract prevents safely adding " + p.name + ". Keep the existing choice and configure the additional connection manually, or explicitly disable competing clients if you intend this target, then run self-host setup " + from.App + ". Setup does not change priorities, tags, indexer assignments or enabled flags. Preserve existing jobs and configuration."}
	classification := p
	if p.protocolFamily == downloadProtocolSingle && p.target == "qbittorrent" && p.implementation == "QBittorrent" && p.connectionFields == nil {
		classification.protocolFamily = downloadProtocolTorrent
	}
	if classification.protocolFamily == downloadProtocolSingle || !classification.admitsDownloadProtocol(from, template["protocol"]) {
		return refusal
	}
	if classification.protocolFamily == downloadProtocolUsenet {
		classification.protocolFamily = downloadProtocolTorrent
	} else {
		classification.protocolFamily = downloadProtocolUsenet
	}
	for _, item := range items {
		enabled, ok := item["enable"].(bool)
		if !ok {
			return refusal
		}
		if !enabled {
			continue
		}
		if !classification.admitsDownloadProtocol(from, item["protocol"]) {
			return refusal
		}
		contract, ok := item["configContract"].(string)
		if !ok || strings.TrimSpace(contract) == "" {
			return refusal
		}
		var schema provider
		for _, candidate := range schemas {
			if candidate["implementation"] != item["implementation"] {
				continue
			}
			if schema != nil || candidate["configContract"] != contract {
				return refusal
			}
			schema = candidate
		}
		if schema == nil || p.matchDownloadSchema(from, item, schema) != nil {
			return refusal
		}
	}
	return nil
}

func (p providerLink) matchDownloadSchema(h *helpers.H, item, template provider) error {
	if item["protocol"] != template["protocol"] {
		return p.manual(h, "the recorded download-client protocol does not exactly match the current native schema; preserve the resource and review consumer compatibility")
	}
	values := map[string]any{}
	for _, raw := range item["fields"].([]any) {
		field := raw.(map[string]any)
		values[field["name"].(string)] = field["value"]
	}
	if len(values) != len(template["fields"].([]any)) {
		return p.manual(h, "the recorded download-client fields do not match the current native schema")
	}
	for _, raw := range template["fields"].([]any) {
		field := raw.(map[string]any)
		actual, exists := values[field["name"].(string)]
		if !exists {
			return p.manual(h, "a recorded download-client schema field is unavailable")
		}
		expected := field["value"]
		if expected == nil {
			if actual == nil {
				continue
			}
			if _, ok := actual.(string); ok {
				continue
			}
			return p.manual(h, "a recorded download-client schema field has an unsupported type")
		}
		if actual == nil {
			if _, ok := expected.(string); ok {
				continue
			}
		}
		if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
			return p.manual(h, "a recorded download-client schema field has an unsupported type")
		}
	}
	return nil
}

func credentialField(name string) bool { return name == "apiKey" || name == "password" }

func downloadFieldEqual(actual, expected any) bool {
	return reflect.DeepEqual(actual, expected) || actual == nil && expected == ""
}

func (p providerLink) downloadIdentity(from *helpers.H, item provider) bool {
	return item["implementation"] == p.implementation && item["configContract"] == p.configContract && p.admitsDownloadProtocol(from, item["protocol"]) && item["enable"] == true
}

func (p providerLink) verifyDownload(ctx context.Context, from, to *helpers.H, item provider, fields []connectionField) error {
	if !p.downloadIdentity(from, item) {
		return p.manual(from, "the recorded download-client identity or enabled state changed")
	}
	template, err := p.downloadTemplate(ctx, from)
	if err != nil {
		return err
	}
	if p.protocolFamily != downloadProtocolSingle {
		if err := p.matchDownloadSchema(from, item, template); err != nil {
			return err
		}
	}
	for _, field := range fields {
		schemaValue := template.field(field.Name)
		if schemaValue == nil {
			if _, ok := field.Value.(string); !ok {
				return p.manual(from, "a required download-client schema field has an unsupported type")
			}
		} else if reflect.TypeOf(schemaValue) != reflect.TypeOf(field.Value) {
			return p.manual(from, "a required download-client schema field has an unsupported type")
		}
		if err := template.setField(field.Name, field.Value); err != nil {
			return p.manual(from, "a required download-client schema field is unavailable")
		}
		actual := item.field(field.Name)
		if credentialField(field.Name) {
			if _, ok := actual.(string); !ok {
				return p.manual(from, "the recorded download-client credential field is unreadable")
			}
			continue
		}
		if !downloadFieldEqual(actual, field.Value) {
			return p.manual(from, "the recorded download-client connection was edited; preserve its choices and review it explicitly")
		}
	}
	category, ok := item.field(p.categoryField).(string)
	if !ok || strings.TrimSpace(category) == "" || strings.TrimSpace(category) != category || strings.ContainsAny(category, "\r\n\x00") {
		return p.manual(from, "choose an explicit isolated download category before retrying Setup")
	}
	if value := template.field(p.categoryField); value != nil {
		if _, ok := value.(string); !ok {
			return p.manual(from, "the download-client category schema has an unsupported type")
		}
	}
	if err := template.setField(p.categoryField, category); err != nil {
		return p.manual(from, "the download-client category schema is unavailable")
	}
	if p.verify == nil {
		return fmt.Errorf("the %s download-client verifier is unavailable", p.name)
	}
	return p.verify(ctx, from, to, item)
}

func (p providerLink) testDownload(ctx context.Context, h *helpers.H, item provider) error {
	resp, err := p.request(ctx, h, "POST", p.path+"/test", item)
	if err != nil {
		return fmt.Errorf("the %s connection test could not be completed; retry self-host setup %s", p.name, h.App)
	}
	if resp.Status == 204 && len(strings.TrimSpace(string(resp.Body))) == 0 {
		return nil
	}
	if resp.Status == 200 {
		var result any
		if json.Unmarshal(resp.Body, &result) == nil {
			switch value := result.(type) {
			case map[string]any:
				if len(value) == 0 {
					return nil
				}
			case string:
				if value == "{}" {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("the %s connection test returned an unsuccessful or invalid result (HTTP %d); review Settings > Download Clients and retry self-host setup %s", p.name, resp.Status, h.App)
}

func (p providerLink) downloadCurrent(ctx context.Context, from, to *helpers.H, item provider, record providerOwnership, fields []connectionField) (bool, error) {
	if err := p.verifyDownload(ctx, from, to, item, fields); err != nil {
		return false, err
	}
	current, err := p.downloadCredentialsCurrent(from, item, record, fields)
	if err != nil || !current {
		return false, err
	}
	return true, p.testDownload(ctx, from, item)
}

func (p providerLink) downloadCredentialsCurrent(h *helpers.H, item provider, record providerOwnership, fields []connectionField) (bool, error) {
	current, known := true, true
	observed := make([]connectionField, 0, len(fields))
	for _, field := range fields {
		actual := item.field(field.Name)
		observed = append(observed, connectionField{Name: field.Name, Value: actual})
		if downloadFieldEqual(actual, field.Value) {
			continue
		}
		if credentialField(field.Name) && actual == "********" {
			known = false
			if record.Fingerprint == connectionFingerprint(fields) {
				continue
			}
		}
		current = false
	}
	if !current && known && connectionFingerprint(observed) != record.Fingerprint {
		return false, p.manual(h, "the recorded download-client credentials were edited; restore the intended credentials explicitly before retrying")
	}
	return current, nil
}

func (p providerLink) updateDownload(ctx context.Context, from, to *helpers.H, item provider, record providerOwnership, fields []connectionField) error {
	if err := p.verifyDownload(ctx, from, to, item, fields); err != nil {
		return err
	}
	current, err := p.downloadCredentialsCurrent(from, item, record, fields)
	if err != nil {
		return err
	}
	if current {
		return p.testDownload(ctx, from, item)
	}
	for _, field := range fields {
		if credentialField(field.Name) {
			if err := item.setField(field.Name, field.Value); err != nil {
				return err
			}
		}
	}
	if err := p.testDownload(ctx, from, item); err != nil {
		return err
	}
	resp, err := p.request(ctx, from, "PUT", p.path+"/"+fmt.Sprint(record.ID), item)
	if err != nil {
		return fmt.Errorf("the recorded %s credential update could not be verified; retry Setup without replacing the resource", p.name)
	}
	var updated provider
	if resp.Status != 200 && resp.Status != 202 || resp.Decode(&updated) != nil || updated.id() != record.ID || !p.downloadIdentity(from, updated) || updated["protocol"] != item["protocol"] || updated.field(p.endpointField) != item.field(p.endpointField) || updated.field(p.categoryField) != item.field(p.categoryField) {
		return fmt.Errorf("the recorded %s credential update did not return its expected identity; retry Setup without changing ownership", p.name)
	}
	record.Fingerprint = connectionFingerprint(fields)
	return p.save(from, record)
}
