package homarr

import (
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	switch c.Kind {
	case "coolify":
		return coolifyExternalTarget(c)
	case "proxmox":
		return proxmoxExternalTarget(c)
	}
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}
