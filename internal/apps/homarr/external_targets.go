package homarr

import (
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

func externalTarget(c helpers.ExternalConnection) (target, error) {
	return target{}, errors.New("unsupported external integration kind; use a supported exact Homarr kind")
}
