package homarr

import (
	"errors"

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
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}
