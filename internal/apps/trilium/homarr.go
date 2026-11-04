package trilium

import (
	"context"

	"github.com/ajnart/self-host/internal/helpers"
)

func VerifyHomarr(context.Context, *helpers.H) error {
	return &helpers.ManualError{Hint: HomarrRecoveryHint}
}
