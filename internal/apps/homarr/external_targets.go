package homarr

import (
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	switch c.Kind {
	case "jellyseerr":
		return jellyseerrExternalTarget(c)
	case "overseerr":
		return overseerrExternalTarget(c)
	case "proxmox":
		return proxmoxExternalTarget(c)
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}
