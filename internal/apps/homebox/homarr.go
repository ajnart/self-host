package homebox

import (
	"context"
	"fmt"

	"github.com/ajnart/self-host/internal/helpers"
)

const HomarrRecoveryHint = "Homarr reads Homebox statistics with the existing account API key and never issues or rotates it. Run self-host setup homebox, then restore the known matching key and account_identity through the private inputs homebox.api_key and homebox.account_identity using install --set-file and owner-only credential files, after reviewing and separately approving any Compose replacement. Never rotate the pepper, delete keys by name or switch the default group. Homebox API keys have no read-only scope and inherit the owner's write access; this integration only reads."

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("api_key")) == "" {
		return &helpers.ManualError{Hint: "Homebox has no account API key for Homarr; key issuance stays Manual. " + HomarrRecoveryHint}
	}
	return verifyAccount(ctx, h, false)
}

func HomarrAccountID(h *helpers.H) (string, error) {
	identity, saved, err := savedIdentity(h)
	if err != nil {
		return "", err
	}
	if !saved {
		return "", fmt.Errorf("%w: Homebox has no verified account identity; run self-host setup homebox", helpers.ErrManual)
	}
	return identity.AccountID, nil
}
