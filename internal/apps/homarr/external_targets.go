package homarr

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

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
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(endpoint, "\r\n\t\\ #") {
		return errors.New(label + " requires an approved trusted-HTTPS origin without embedded credentials, API/proxy paths, query or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("the private " + label + " endpoint has an invalid port")
		}
	}
	return nil
}
