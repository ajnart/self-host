package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const syncthingRelayRecovery = "Syncthing Relay Stats read only the relay's HTTP status listener, normally port 22070, with no credentials. Preserve this connection, confirm that the status base endpoint is reachable by self-host and Homarr without a protected proxy, then retry self-host setup homarr. The relay token is not status authentication and no relay Device ID is attested; do not use the GUI, join a pool, open a public port, pair devices or bypass TLS."

func syncthingRelayExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "syncthingRelay" {
		return target{}, errors.New("the Syncthing Relay external adapter requires the exact syncthingRelay kind")
	}
	if err := externalHTTPOrigin(c.Endpoint, "Syncthing Relay"); err != nil {
		return target{}, err
	}
	if len(c.Credentials) != 0 || len(c.Selection) != 0 {
		return target{}, errors.New("Syncthing Relay status is unauthenticated; supply no credentials or selection")
	}
	read := func(ctx context.Context, from *helpers.H) error {
		manual := &helpers.ManualError{Hint: syncthingRelayRecovery}
		resp, err := from.HTTP(ctx, helpers.Request{BaseURL: c.Endpoint, Path: "/status", NoRedirect: true})
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if err != nil || resp.Status != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
			return manual
		}
		var status map[string]json.RawMessage
		if json.Unmarshal(resp.Body, &status) != nil {
			return manual
		}
		var version string
		var startTime string
		if json.Unmarshal(status["version"], &version) != nil || version == "" || json.Unmarshal(status["startTime"], &startTime) != nil || startTime == "" {
			return manual
		}
		for _, key := range syncthingRelayMetrics {
			var number *float64
			if json.Unmarshal(status[key], &number) != nil || number == nil {
				return manual
			}
			if _, ok := measuredNumber(*number, true); !ok {
				return manual
			}
		}
		return nil
	}
	return target{
		name: "Syncthing Relay", kind: "syncthingRelay",
		widgets: []widget{statWidget(syncthingRelayMetrics...)},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			return read(ctx, from)
		},
		verify: func(ctx context.Context, from, _ *helpers.H, _ string) error {
			return read(ctx, from)
		},
		stats: syncthingRelayValidateStats,
	}, nil
}

var syncthingRelayMetrics = []string{"numActiveSessions", "numConnections", "bytesProxied"}

func syncthingRelayValidateStats(sample statsSample) error {
	retry, retryOK := sample.retryAt.(float64)
	valid := retryOK && retry == 0 && validStatsTimestamp(sample.updatedAt) && sample.updatedAt <= sample.completedAt+5000 && sample.updatedAt >= sample.startedAt-60000
	for key, unit := range map[string]string{"numActiveSessions": "count", "numConnections": "count", "bytesProxied": "bytes"} {
		_, ok := measuredNumber(sample.values[key], true)
		valid = valid && ok && sample.units[key] == unit
	}
	if !valid {
		return &helpers.ManualError{Hint: "Syncthing Relay Stats require measured active-session, connection and proxied-byte counters (zero is valid), count/bytes units and one genuinely advancing refresh with valid clock/retry metadata. " + syncthingRelayRecovery}
	}
	return nil
}
