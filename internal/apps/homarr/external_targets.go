package homarr

import (
	"errors"
	"slices"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	switch c.Kind {
	case "ical":
		return icalExternalTarget(c)
	case "coolify":
		return coolifyExternalTarget(c)
	case "jellyseerr":
		return jellyseerrExternalTarget(c)
	case "overseerr":
		return overseerrExternalTarget(c)
	case "llamacpp":
		return llamacppExternalTarget(c)
	case "proxmox":
		return proxmoxExternalTarget(c)
	case "synology":
		return synologyExternalTarget(c)
	case "opnsense":
		return opnsenseExternalTarget(c)
	case "netalertx":
		return netalertxExternalTarget(c)
	case "unraid":
		return unraidExternalTarget(c)
	case "truenas":
		return truenasExternalTarget(c)
	case "unifiController":
		return unifiControllerExternalTarget(c)
	case "wazuh":
		return wazuhExternalTarget(c)
	case "openmediavault":
		return openmediavaultExternalTarget(c)
	case "scrutiny":
		return scrutinyExternalTarget(c)
	case "readarr":
		return readarrExternalTarget(c)
	case "syncthingRelay":
		return syncthingRelayExternalTarget(c)
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}

func externalHTTPSOrigin(endpoint, label string) error {
	return externalOrigin(endpoint, label, "trusted-HTTPS", "https")
}

func externalHTTPOrigin(endpoint, label string) error {
	return externalOrigin(endpoint, label, "HTTP(S)", "http", "https")
}

func externalOrigin(endpoint, label, kind string, schemes ...string) error {
	if u, ok := helpers.HTTPOrigin(endpoint); !ok || !slices.Contains(schemes, u.Scheme) {
		return errors.New(label + " requires an approved " + kind + " origin without embedded credentials, API/proxy paths, query or fragment")
	}
	return nil
}
