package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

const tandoorNativeHint = "Managed Tandoor linking is held: the supported native APIs do not prove the exact saved token's identity, rights and expiry. Preserve existing Homarr credentials, integrations, App records and Tandoor Spaces. Do not edit upstream credentials, decrypt secrets, access private databases or replace a token to bypass this hold. Native linking requires a reviewed authoritative token-specific contract before any mutation or Stats refresh."

func tandoorNativeHold() error { return &helpers.ManualError{Hint: tandoorNativeHint} }

func tandoorTarget() target {
	return target{
		id: "tandoor", name: "Tandoor", kind: "tandoor", internal: (*helpers.H).InternalURL,
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets:   []widget{statWidget("users", "recipes", "keywords")},
		preflight: func(context.Context, *helpers.H, *helpers.H) error { return tandoorNativeHold() },
		verify:    verifyTandoor,
		stats:     verifyTandoorStats,
	}
}

func verifyTandoor(context.Context, *helpers.H, *helpers.H, string) error {
	return tandoorNativeHold()
}

func verifyTandoorStats(sample statsSample) error {
	users, valid := countValue(sample.values["users"])
	if !validCountStats(sample, "users", "recipes", "keywords") || !valid || users < 1 || sample.updatedAt < sample.startedAt-60_000 || sample.updatedAt > sample.completedAt+60_000 {
		return errors.New("Homarr returned invalid Tandoor Space counts; retry self-host setup homarr")
	}
	if sample.retryAt != nil && sample.retryAt != float64(0) {
		return errors.New("Homarr deferred Tandoor statistics; retry self-host setup homarr")
	}
	return nil
}
