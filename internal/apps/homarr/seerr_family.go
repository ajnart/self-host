package homarr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

type seerrFamily struct {
	name      string
	kind      string
	marker    string
	partition bool
}

var seerrFamilyCountKeys = []string{"total", "movie", "tv", "pending", "approved", "declined", "processing", "available"}
var seerrFamilyStatsKeys = []string{"pending", "approved", "available", "processing"}

func (f seerrFamily) transportHold() string {
	return f.name + " native transport is held before integration creation: Homarr's native fetch sets no redirect override, so a successful CLI no-redirect probe cannot establish that the API key stays confined to the approved origin. Preserve this connection and its key. Retry self-host setup homarr only after an audited adapter establishes fail-closed redirect and trusted-TLS behavior. Do not use a proxy, disable TLS verification, rotate credentials or change " + f.name + " to bypass this hold."
}

func (f seerrFamily) dataRecovery() string {
	return f.name + "'s authenticated request counts are not verified. Preserve its endpoint, API key, libraries, media-server choice and requests; check that the key belongs to an initialized " + f.name + " endpoint with trusted TLS, then retry self-host setup homarr. Missing or null counts are not measured zeros."
}

func (f seerrFamily) externalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != f.kind {
		return target{}, fmt.Errorf("the %s external adapter requires the exact %s kind", f.name, f.kind)
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, fmt.Errorf("%s requires an approved trusted-HTTPS origin without embedded credentials, API/proxy paths, query or fragment", f.name)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return target{}, fmt.Errorf("the private %s endpoint has an invalid port", f.name)
		}
	}
	key := ""
	for _, credential := range c.Credentials {
		if key != "" || credential.Kind != "apiKey" || credential.Value == "" || strings.TrimSpace(credential.Value) != credential.Value || strings.ContainsAny(credential.Value, "\r\n\t ") {
			return target{}, fmt.Errorf("%s requires exactly one nonempty apiKey credential; supply it only through the owner-only private input", f.name)
		}
		key = credential.Value
	}
	if key == "" {
		return target{}, fmt.Errorf("%s requires exactly one nonempty apiKey credential; supply it only through the owner-only private input", f.name)
	}
	if len(c.Selection) != 0 {
		return target{}, fmt.Errorf("%s has no selection; its media-server choice, libraries and requests are preserved as configured", f.name)
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	return target{
		name: f.name, kind: f.kind,
		widgets: []widget{statWidget(seerrFamilyStatsKeys...)},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			if err := f.probeIdentity(ctx, from, endpoint, key); err != nil {
				return err
			}
			return &helpers.ManualError{Hint: f.transportHold()}
		},
		verify: func(ctx context.Context, from, _ *helpers.H, _ string) error {
			return f.verifyCounts(ctx, from, endpoint, key)
		},
		stats: f.validateStats,
	}, nil
}

func (f seerrFamily) get(ctx context.Context, from *helpers.H, endpoint, path, key string) (helpers.Response, error) {
	request := helpers.Request{Path: path, BaseURL: endpoint, NoRedirect: true}
	if key != "" {
		request.Header = map[string]string{"X-Api-Key": key}
	}
	return from.HTTP(ctx, request)
}

func (f seerrFamily) probeIdentity(ctx context.Context, from *helpers.H, endpoint, key string) error {
	manual := func(reason string) error {
		return &helpers.ManualError{Hint: f.name + " identity is not verified: " + reason + ". Preserve the endpoint and key, review them with self-host connections update <id> --file PATH --yes, then run self-host setup homarr. Never initialize " + f.name + ", disable authentication or bypass TLS."}
	}
	resp, err := f.get(ctx, from, endpoint, "/api/v1", "")
	if err != nil {
		return err
	}
	var root struct {
		API *string `json:"api"`
	}
	if resp.Status != 200 || resp.Decode(&root) != nil || root.API == nil || *root.API != f.marker {
		return manual("the endpoint is not a " + f.name + " API")
	}
	resp, err = f.get(ctx, from, endpoint, "/api/v1/status", "")
	if err != nil {
		return err
	}
	var version struct {
		Version *string `json:"version"`
	}
	if resp.Status != 200 || resp.Decode(&version) != nil || version.Version == nil || *version.Version == "" {
		return manual("it reports no version")
	}
	resp, err = f.get(ctx, from, endpoint, "/api/v1/settings/public", "")
	if err != nil {
		return err
	}
	var public struct {
		Initialized     *bool `json:"initialized"`
		MediaServerType *int  `json:"mediaServerType"`
	}
	if resp.Status != 200 || resp.Decode(&public) != nil || public.Initialized == nil || public.MediaServerType == nil || *public.MediaServerType < 1 || *public.MediaServerType > 4 {
		return manual("its public settings are incomplete")
	}
	if !*public.Initialized {
		return manual("it is not initialized; finish its own setup first")
	}
	resp, err = f.get(ctx, from, endpoint, "/api/v1/auth/me", key)
	if err != nil {
		return err
	}
	if resp.Status == 401 || resp.Status == 403 {
		return manual("it rejected the API key")
	}
	var me struct {
		ID *float64 `json:"id"`
	}
	if resp.Status != 200 || resp.Decode(&me) != nil || me.ID == nil || *me.ID < 1 || *me.ID != math.Trunc(*me.ID) || *me.ID > 1<<53-1 {
		return manual("the key does not identify a numeric account")
	}
	return nil
}

func (f seerrFamily) verifyCounts(ctx context.Context, from *helpers.H, endpoint, key string) error {
	invalid := func() error { return &helpers.ManualError{Hint: f.dataRecovery()} }
	resp, err := f.get(ctx, from, endpoint, "/api/v1/request/count", key)
	if err != nil {
		return err
	}
	var counts map[string]any
	if resp.Status != 200 || resp.Decode(&counts) != nil {
		return invalid()
	}
	values := map[string]float64{}
	for _, name := range seerrFamilyCountKeys {
		number, ok := seerrFamilyCount(counts[name])
		if !ok {
			return invalid()
		}
		values[name] = number
	}
	if !f.partitionHolds(values) {
		return invalid()
	}
	return nil
}

func (f seerrFamily) partitionHolds(values map[string]float64) bool {
	return !f.partition || values["processing"]+values["available"] == values["approved"]
}

func seerrFamilyCount(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && number == math.Trunc(number) && number <= 1<<53-1
}

func (f seerrFamily) validateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	if !retryOK || retry != 0 || !validStatsTimestamp(sample.updatedAt) {
		return errors.New("Homarr returned invalid " + f.name + " Stats refresh metadata; retry self-host setup homarr")
	}
	invalid := errors.New("Homarr returned invalid " + f.name + " request count statistics; retry self-host setup homarr")
	values := map[string]float64{}
	for _, key := range seerrFamilyStatsKeys {
		number, ok := seerrFamilyCount(sample.values[key])
		if !ok || sample.units[key] != "count" {
			return invalid
		}
		values[key] = number
	}
	if !f.partitionHolds(values) {
		return invalid
	}
	return nil
}
