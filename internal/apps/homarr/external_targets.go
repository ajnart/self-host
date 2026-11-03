package homarr

import (
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	switch c.Kind {
	case "proxmox":
		return proxmoxExternalTarget(c)
	case "scrutiny":
		return scrutinyExternalTarget(c)
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}
