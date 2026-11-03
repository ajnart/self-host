package servarr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

type provider map[string]any

func (p provider) field(name string) any {
	fields, _ := p["fields"].([]any)
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		if field["name"] == name {
			return field["value"]
		}
	}
	return nil
}

func (p provider) setField(name string, value any) error {
	fields, _ := p["fields"].([]any)
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		if field["name"] == name {
			field["value"] = value
			return nil
		}
	}
	return fmt.Errorf("the schema has no field %q", name)
}

func (p provider) id() int64 {
	id, ok := p["id"].(float64)
	if !ok || !(id > 0 && id <= 2147483647) || id != float64(int64(id)) {
		return 0
	}
	return int64(id)
}

type connectionField struct {
	Name  string
	Value any
}

type providerOwnership struct {
	ID          int64  `json:"id"`
	Target      string `json:"target"`
	Endpoint    string `json:"endpoint"`
	Fingerprint string `json:"fingerprint"`
}

type providerLink struct {
	api, path, target, implementation, name string
	endpointField, categoryField, category  string
	configContract, protocol                string
	protocolFamily                          downloadProtocolFamily
	connectionFields                        func(from, to *helpers.H) ([]connectionField, error)
	verify                                  func(ctx context.Context, from, to *helpers.H, item provider) error
}

func QBittorrentLink(api, categoryField, category string) app.Link {
	p := providerLink{api: api, path: "/downloadclient", target: "qbittorrent", implementation: "QBittorrent", name: "qBittorrent", endpointField: "host", categoryField: categoryField, category: category}
	return p.link("download client: qBittorrent", app.LinkStep{
		Message: "use qBittorrent as the download client",
		Hint: func(from, to *helpers.H) string {
			return "Open " + from.URL + " > Settings > Download Clients > add qBittorrent with host qbittorrent, port " + strconv.Itoa(to.Port) + " and the Admin login."
		},
	}, app.LinkStep{
		Message: "remove the qBittorrent download client",
		Hint: func(from, _ *helpers.H) string {
			return "Open " + from.URL + " > Settings > Download Clients and delete qBittorrent."
		},
	})
}

func ApplicationLink(target, implementation string) app.Link {
	p := providerLink{api: V1, path: "/applications", target: target, implementation: implementation, name: implementation, endpointField: "baseUrl"}
	return p.link("sync indexers to "+implementation, app.LinkStep{
		Message: "sync indexers to " + implementation,
		Hint: func(from, to *helpers.H) string {
			return "Open " + from.URL + " > Settings > Apps > add " + implementation + " with Prowlarr server " + from.InternalURL() + ", server " + to.InternalURL() + " and its API key."
		},
	}, app.LinkStep{
		Message: "stop syncing indexers to " + implementation,
		Hint: func(from, _ *helpers.H) string {
			return "Open " + from.URL + " > Settings > Apps and delete " + implementation + "."
		},
	})
}

func (p providerLink) link(title string, apply, remove app.LinkStep) app.Link {
	id := "link-" + p.target
	run := func(ctx context.Context, from, to *helpers.H, removing bool, step app.LinkStep) error {
		stepID := id
		if removing {
			stepID = "un" + id
		}
		var checkErr error
		return from.Step(ctx, stepID, step.Message, helpers.StepSpec{
			Check: func(ctx context.Context) (bool, error) {
				done, err := p.current(ctx, from, to, removing)
				checkErr = err
				return done, err
			},
			Do: func(ctx context.Context) error {
				if checkErr != nil {
					return checkErr
				}
				return p.reconcile(ctx, from, to, removing)
			},
			Hint: step.Hint(from, to), Credential: "admin",
		})
	}
	return app.Link{
		ID: id, Target: p.target, Title: title,
		Apply:  func(ctx context.Context, from, to *helpers.H) error { return run(ctx, from, to, false, apply) },
		Remove: func(ctx context.Context, from, to *helpers.H) error { return run(ctx, from, to, true, remove) },
	}
}

func (p providerLink) ownershipKey(h *helpers.H) string {
	return h.Key("link_" + p.target + "_resource")
}

func (p providerLink) manual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: "Review " + h.URL + " > Settings and " + p.ownershipKey(h) + " in the Project's private .env: " + reason + ". Preserve person-owned entries; never claim an ID by matching its name or endpoint. Remove only a confirmed failed self-host creation manually. Clear an uncertain ownership record only after confirming that creation is absent, then run self-host setup " + h.App + "."}
}

func (p providerLink) request(ctx context.Context, h *helpers.H, method, path string, item provider) (helpers.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req := helpers.Request{Method: method, Path: path, NoRedirect: true}
	if item != nil {
		req.JSON = item
	}
	return h.HTTP(ctx, Request(h, p.api, req))
}

func (p providerLink) list(ctx context.Context, h *helpers.H, path string) ([]provider, error) {
	resp, err := p.request(ctx, h, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("read %s returned HTTP %d; retry without changing Link ownership", path, resp.Status)
	}
	var items []provider
	if err := resp.Decode(&items); err != nil || items == nil {
		return nil, fmt.Errorf("%s returned an invalid resource list", path)
	}
	schema := path == p.path+"/schema"
	ids := map[int64]bool{}
	for _, item := range items {
		implementation, _ := item["implementation"].(string)
		fields, ok := item["fields"].([]any)
		if strings.TrimSpace(implementation) == "" || !ok || len(fields) == 0 {
			return nil, fmt.Errorf("%s returned a resource without a valid implementation and fields", path)
		}
		names := map[string]bool{}
		for _, raw := range fields {
			field, ok := raw.(map[string]any)
			name, _ := field["name"].(string)
			if !ok || strings.TrimSpace(name) == "" || names[name] {
				return nil, fmt.Errorf("%s returned malformed or duplicate resource fields", path)
			}
			names[name] = true
		}
		if schema {
			continue
		}
		id := item.id()
		name, _ := item["name"].(string)
		if id == 0 || ids[id] || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s returned an invalid or duplicate resource identity", path)
		}
		ids[id] = true
		if implementation == p.implementation {
			endpoint, _ := item.field(p.endpointField).(string)
			if strings.TrimSpace(endpoint) == "" {
				return nil, fmt.Errorf("%s returned a resource without its connection endpoint", path)
			}
		}
	}
	return items, nil
}

func (p providerLink) pointsAtTarget(endpoint string, to *helpers.H) bool {
	host := endpoint
	if p.path == "/applications" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return false
		}
		host = parsed.Hostname()
	}
	return strings.EqualFold(strings.TrimSuffix(host, "."), p.target) || (to.Service != "" && strings.EqualFold(strings.TrimSuffix(host, "."), to.Service))
}

func (p providerLink) read(ctx context.Context, from, to *helpers.H, removing bool) (provider, providerOwnership, error) {
	var record providerOwnership
	if to.App != p.target {
		return nil, record, p.manual(from, "the Link target does not match its App")
	}
	if raw := from.Secret(p.ownershipKey(from)); raw != "" {
		if err := json.Unmarshal([]byte(raw), &record); err != nil || record.ID <= 0 || record.ID > 2147483647 || record.Target != p.target || !p.pointsAtTarget(record.Endpoint, to) || len(record.Fingerprint) != 64 {
			return nil, record, p.manual(from, "the saved ownership is invalid or a previous creation has an uncertain outcome")
		}
	}
	if p.connectionFields != nil && removing && record.ID == 0 {
		return nil, record, nil
	}
	var items []provider
	var err error
	if p.connectionFields != nil {
		items, err = p.downloadList(ctx, from, p.path)
	} else {
		items, err = p.list(ctx, from, p.path)
	}
	if err != nil {
		return nil, record, err
	}
	var found provider
	for _, item := range items {
		endpoint, _ := item.field(p.endpointField).(string)
		if record.ID > 0 && item.id() == record.ID {
			if found != nil || item["implementation"] != p.implementation || !p.pointsAtTarget(endpoint, to) || p.connectionFields != nil && (item["configContract"] != p.configContract || !p.admitsDownloadProtocol(from, item["protocol"])) {
				return nil, record, p.manual(from, "the recorded ID no longer uniquely identifies the expected implementation and target")
			}
			found = item
			continue
		}
		if p.connectionFields != nil && removing {
			continue
		}
		name, _ := item["name"].(string)
		if strings.EqualFold(name, p.name) || p.pointsAtTarget(endpoint, to) {
			return nil, record, p.manual(from, "an unowned entry already uses this name or target")
		}
	}
	if p.connectionFields != nil && p.protocolFamily != downloadProtocolSingle {
		template, err := p.downloadTemplate(ctx, from)
		if err != nil {
			return nil, record, err
		}
		if found != nil {
			if err := p.matchDownloadSchema(from, found, template); err != nil {
				return nil, record, err
			}
		}
	}
	return found, record, nil
}

func (p providerLink) connection(from, to *helpers.H) ([]connectionField, error) {
	if p.connectionFields != nil {
		return p.connectionFields(from, to)
	}
	if p.path == "/applications" {
		key := to.Secret(to.Key("api_key"))
		if from.InternalURL() == "" || to.InternalURL() == "" || key == "" {
			return nil, fmt.Errorf("the %s Link needs both internal Addresses and the target's accepted API key", p.name)
		}
		return []connectionField{{"prowlarrUrl", from.InternalURL()}, {"baseUrl", to.InternalURL()}, {"apiKey", key}}, nil
	}
	if to.Port <= 0 || to.Port > 65535 || from.AdminUser() == "" || from.AdminPassword() == "" {
		return nil, fmt.Errorf("the qBittorrent Link needs its port and the Admin login")
	}
	return []connectionField{{"host", "qbittorrent"}, {"port", float64(to.Port)}, {"username", from.AdminUser()}, {"password", from.AdminPassword()}}, nil
}

func connectionFingerprint(fields []connectionField) string {
	raw, _ := json.Marshal(fields)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func connectionCurrent(item provider, record providerOwnership, fields []connectionField) (bool, bool) {
	masked := false
	for _, field := range fields {
		actual := item.field(field.Name)
		if actual == field.Value {
			continue
		}
		if (field.Name == "apiKey" || field.Name == "password") && actual == "********" && record.Fingerprint == connectionFingerprint(fields) {
			masked = true
			continue
		}
		return false, false
	}
	return true, masked
}

func (p providerLink) current(ctx context.Context, from, to *helpers.H, removing bool) (bool, error) {
	item, record, err := p.read(ctx, from, to, removing)
	if err != nil {
		return false, err
	}
	if removing {
		return item == nil && record.ID == 0, nil
	}
	if item == nil {
		if record.ID > 0 {
			return false, p.manual(from, "the recorded resource is missing; automatic replacement could claim or duplicate a person-owned entry")
		}
		return false, nil
	}
	if p.path == "/downloadclient" && item["enable"] != true {
		return false, p.manual(from, "the recorded download client is disabled or its enabled state is unreadable; enable it explicitly only if wanted")
	}
	fields, err := p.connection(from, to)
	if err != nil {
		return false, err
	}
	if p.connectionFields != nil {
		return p.downloadCurrent(ctx, from, to, item, record, fields)
	}
	current, masked := connectionCurrent(item, record, fields)
	if current && masked {
		resp, err := p.request(ctx, from, "POST", p.path+"/test", item)
		if err != nil {
			return false, err
		}
		if credentialVerificationFailed(resp) {
			return false, nil
		}
		if resp.Status != 200 && resp.Status != 204 {
			return false, fmt.Errorf("the owned %s connection test returned HTTP %d; review its credentials and retry", p.name, resp.Status)
		}
	}
	return current, nil
}

func credentialVerificationFailed(resp helpers.Response) bool {
	if resp.Status != 400 {
		return false
	}
	var failures []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if resp.Decode(&failures) != nil || len(failures) == 0 {
		return false
	}
	for _, failure := range failures {
		if strings.TrimSpace(failure.ErrorMessage) == "" {
			return false
		}
		switch strings.ToLower(failure.PropertyName) {
		case "apikey", "username", "password":
		default:
			return false
		}
	}
	return true
}

func (p providerLink) save(h *helpers.H, record providerOwnership) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return h.SetSecret(p.ownershipKey(h), string(raw))
}

func (p providerLink) reconcile(ctx context.Context, from, to *helpers.H, removing bool) error {
	item, record, err := p.read(ctx, from, to, removing)
	if err != nil {
		return err
	}
	if removing {
		if item != nil {
			resp, err := p.request(ctx, from, "DELETE", p.path+"/"+strconv.FormatInt(record.ID, 10), nil)
			if err != nil {
				return err
			}
			if resp.Status != 200 && resp.Status != 204 && resp.Status != 404 {
				return fmt.Errorf("remove the recorded %s resource returned HTTP %d", p.name, resp.Status)
			}
			remaining, _, err := p.read(ctx, from, to, true)
			if err != nil {
				return err
			}
			if remaining != nil {
				return fmt.Errorf("the recorded %s resource remains after deletion", p.name)
			}
		}
		return from.SetSecret(p.ownershipKey(from), "")
	}
	if item != nil && p.path == "/downloadclient" && item["enable"] != true {
		return p.manual(from, "the recorded download client was disabled before reconciliation; enable it explicitly only if wanted")
	}
	fields, err := p.connection(from, to)
	if err != nil {
		return err
	}
	if item == nil {
		if record.ID > 0 {
			return p.manual(from, "the recorded resource disappeared before reconciliation")
		}
		return p.create(ctx, from, to, fields)
	}
	if p.connectionFields != nil {
		return p.updateDownload(ctx, from, to, item, record, fields)
	}
	if current, masked := connectionCurrent(item, record, fields); current && !masked {
		return nil
	}
	for _, field := range fields {
		if err := item.setField(field.Name, field.Value); err != nil {
			return err
		}
	}
	resp, err := p.request(ctx, from, "PUT", p.path+"/"+strconv.FormatInt(record.ID, 10), item)
	if err != nil {
		return err
	}
	if resp.Status != 200 && resp.Status != 202 {
		return fmt.Errorf("update the recorded %s resource returned HTTP %d", p.name, resp.Status)
	}
	record.Endpoint, _ = item.field(p.endpointField).(string)
	record.Fingerprint = connectionFingerprint(fields)
	return p.save(from, record)
}

func (p providerLink) create(ctx context.Context, from, to *helpers.H, fields []connectionField) error {
	var item provider
	if p.connectionFields != nil {
		var err error
		item, err = p.downloadTemplate(ctx, from)
		if err != nil {
			return err
		}
	} else {
		templates, err := p.list(ctx, from, p.path+"/schema")
		if err != nil {
			return err
		}
		for _, template := range templates {
			if template["implementation"] == p.implementation {
				item = template
				break
			}
		}
	}
	if item == nil {
		return fmt.Errorf("%s has no %s implementation", from.App, p.implementation)
	}
	item["name"] = p.name
	if p.path == "/applications" {
		item["syncLevel"] = "fullSync"
	} else {
		item["enable"] = true
		if err := item.setField(p.categoryField, p.category); err != nil {
			return err
		}
	}
	for _, field := range fields {
		if err := item.setField(field.Name, field.Value); err != nil {
			return err
		}
	}
	if p.connectionFields != nil {
		if err := p.verifyDownload(ctx, from, to, item, fields); err != nil {
			return err
		}
		if err := p.testDownload(ctx, from, item); err != nil {
			return err
		}
	}
	endpoint, _ := item.field(p.endpointField).(string)
	record := providerOwnership{Target: p.target, Endpoint: endpoint, Fingerprint: connectionFingerprint(fields)}
	if err := p.save(from, record); err != nil {
		return err
	}
	resp, err := p.request(ctx, from, "POST", p.path, item)
	if err != nil {
		return p.manual(from, "creation did not return a verifiable result; do not retry creation without reviewing the remote entries")
	}
	if resp.Status != 200 && resp.Status != 201 {
		switch resp.Status {
		case 400, 401, 403, 404, 405, 409, 422:
			if err := from.SetSecret(p.ownershipKey(from), ""); err != nil {
				return err
			}
			return fmt.Errorf("create the %s resource returned HTTP %d", p.name, resp.Status)
		default:
			return p.manual(from, "creation returned an uncertain response; inspect the remote entries before retrying")
		}
	}
	var created provider
	if err := resp.Decode(&created); err != nil || created.id() == 0 || p.connectionFields == nil && created["implementation"] != p.implementation || created.field(p.endpointField) != endpoint || !p.pointsAtTarget(endpoint, to) {
		return p.manual(from, "creation did not return a positive resource ID with the expected implementation and target")
	}
	if p.connectionFields != nil && (!p.downloadIdentity(from, created) || created["protocol"] != item["protocol"] || created.field(p.categoryField) != item.field(p.categoryField)) {
		return p.manual(from, "creation did not return the expected download-client contract and category")
	}
	record.ID = created.id()
	if err := p.save(from, record); err != nil {
		return p.manual(from, "the created resource ID could not be saved; review the remote entry before retrying")
	}
	return nil
}
