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
	return []app.Link{QBittorrentLink(api, categoryField, category), SABnzbdLink(api, categoryField, category)}
}

func (p providerLink) downloadTemplate(ctx context.Context, h *helpers.H) (provider, error) {
	templates, err := p.list(ctx, h, p.path+"/schema")
	if err != nil {
		return nil, err
	}
	var selected provider
	for _, item := range templates {
		if item["implementation"] != p.implementation {
			continue
		}
		if selected != nil || item["configContract"] != p.configContract || item["protocol"] != p.protocol {
			return nil, p.manual(h, "the download-client schema is ambiguous or unsupported")
		}
		selected = item
	}
	if selected == nil {
		return nil, p.manual(h, "the expected download-client schema is unavailable")
	}
	return selected, nil
}

func credentialField(name string) bool { return name == "apiKey" || name == "password" }

func downloadFieldEqual(actual, expected any) bool {
	return reflect.DeepEqual(actual, expected) || actual == nil && expected == ""
}

func (p providerLink) downloadIdentity(item provider) bool {
	return item["implementation"] == p.implementation && item["configContract"] == p.configContract && item["protocol"] == p.protocol && item["enable"] == true
}

func (p providerLink) verifyDownload(ctx context.Context, from, to *helpers.H, item provider, fields []connectionField) error {
	if !p.downloadIdentity(item) {
		return p.manual(from, "the recorded download-client identity or enabled state changed")
	}
	template, err := p.downloadTemplate(ctx, from)
	if err != nil {
		return err
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
	if resp.Status != 200 && resp.Status != 202 || resp.Decode(&updated) != nil || updated.id() != record.ID || !p.downloadIdentity(updated) || updated.field(p.endpointField) != item.field(p.endpointField) || updated.field(p.categoryField) != item.field(p.categoryField) {
		return fmt.Errorf("the recorded %s credential update did not return its expected identity; retry Setup without changing ownership", p.name)
	}
	record.Fingerprint = connectionFingerprint(fields)
	return p.save(from, record)
}
