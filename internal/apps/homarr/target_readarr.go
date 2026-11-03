package homarr

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const readarrRecovery = "Readarr's explicit existing endpoint, API key or library data is not verified. Preserve this connection; review its private input with self-host connections update <id> --file PATH --yes (API key: Readarr Settings > General > Security), confirm trusted TLS and that the instance is reachable, then retry self-host setup homarr. Readarr is retired and its LinuxServer image deprecated; self-host never installs, replaces or changes it."

func readarrExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "readarr" {
		return target{}, errors.New("the Readarr external adapter requires the exact readarr kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("Readarr requires an explicit http or https origin without embedded credentials, paths, query or fragment")
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "apiKey" || c.Credentials[0].Value == "" {
		return target{}, errors.New("Readarr requires exactly one nonempty apiKey credential supplied through the owner-only private input")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("Readarr takes no selection")
	}
	key := c.Credentials[0].Value
	get := func(ctx context.Context, from *helpers.H, path string, out any) error {
		reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		resp, err := from.HTTP(reqCtx, helpers.Request{
			BaseURL: c.Endpoint, Path: path, NoRedirect: true,
			Header: map[string]string{"X-Api-Key": key},
		})
		if err != nil {
			return err
		}
		if resp.Status != 200 || resp.Decode(out) != nil {
			return &helpers.ManualError{Hint: readarrRecovery}
		}
		return nil
	}
	return target{
		name: "Readarr", kind: "readarr",
		widgets: []widget{plain("calendar"), statWidget("books", "monitored", "upcoming")},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			var status struct {
				AppName string `json:"appName"`
				Version string `json:"version"`
			}
			if err := get(ctx, from, "/api/v1/system/status", &status); err != nil {
				if errors.Is(err, helpers.ErrManual) {
					return err
				}
				return &helpers.ManualError{Hint: readarrRecovery}
			}
			if status.AppName != "Readarr" || status.Version == "" {
				return &helpers.ManualError{Hint: readarrRecovery}
			}
			return nil
		},
		verify: func(ctx context.Context, from, _ *helpers.H, integrationID string) error {
			var books []struct{ Monitored *bool }
			if err := get(ctx, from, "/api/v1/book", &books); err != nil || books == nil {
				return &helpers.ManualError{Hint: readarrRecovery}
			}
			for _, book := range books {
				if book.Monitored == nil {
					return &helpers.ManualError{Hint: readarrRecovery}
				}
			}
			events, err := nativeCalendarEvents(ctx, from, "readarr", "Readarr", integrationID)
			if err != nil {
				return err
			}
			for _, value := range events {
				event, ok := value.(map[string]any)
				if !ok || stringField(event, "title") == "" {
					return errors.New("Homarr returned incomplete Readarr book data; retry self-host setup homarr")
				}
				subtitle, present := event["subTitle"]
				_, text := subtitle.(string)
				if !present || subtitle != nil && !text {
					return errors.New("Homarr returned invalid Readarr author data; retry self-host setup homarr")
				}
				if _, err := time.Parse(time.RFC3339Nano, stringField(event, "startDate")); err != nil {
					return errors.New("Homarr returned an invalid Readarr release date; retry self-host setup homarr")
				}
			}
			return nil
		},
		stats: func(sample statsSample) error { return verifyLibraryCounts(sample, "Readarr", "books") },
	}, nil
}
