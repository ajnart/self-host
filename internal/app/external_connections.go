package app

import (
	"context"

	"github.com/ajnart/self-host/internal/helpers"
)

type ExternalConnectionConsumer interface {
	ValidateExternalConnection(helpers.ExternalConnection) error
	SetupExternalConnections(context.Context, *helpers.H) ([]string, error)
}
