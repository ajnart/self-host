package homarr

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const icalTransportHold = "This Homarr iCal adapter cannot yet guarantee private feed addresses and safe fetching. Keep the feed private; use a reviewed Homarr update that fixes iCal URL/error redaction and fetch restrictions, then retry Homarr Setup. Do not publish the feed, disable TLS checks, or create a replacement integration."

func icalExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "ical" {
		return target{}, errors.New("the iCal external adapter requires the exact ical kind")
	}
	origin, err := url.Parse(c.Endpoint)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.RawFragment != "" || (origin.Path != "" && origin.Path != "/") || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("iCal requires the feed's trusted-HTTPS origin without embedded credentials, path, query or fragment; supply the full feed address only as the private url credential")
	}
	if len(c.Credentials) != 1 || c.Credentials[0].Kind != "url" || c.Credentials[0].Value == "" {
		return target{}, errors.New("iCal requires exactly one nonempty private url credential")
	}
	feed, err := url.Parse(c.Credentials[0].Value)
	if err != nil || feed.Scheme != "https" || feed.User != nil || feed.Fragment != "" || feed.RawFragment != "" || !strings.EqualFold(feed.Host, origin.Host) || strings.ContainsAny(c.Credentials[0].Value, "\r\n\t\\ #") {
		return target{}, errors.New("the iCal url credential must be an absolute trusted-HTTPS feed address on the declared origin without embedded credentials or a fragment")
	}
	if len(c.Selection) != 0 {
		return target{}, errors.New("iCal takes no selection; the read-only feed is the whole connection")
	}
	return target{
		name: "iCal", kind: "ical",
		widgets: []widget{{kind: "calendar", options: map[string]any{}}},
		preflight: func(context.Context, *helpers.H, *helpers.H) error {
			return &helpers.ManualError{Hint: icalTransportHold}
		},
		verify: func(context.Context, *helpers.H, *helpers.H, string) error {
			return &helpers.ManualError{Hint: icalTransportHold}
		},
	}, nil
}
