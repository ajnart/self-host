package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/xteve"
	"github.com/ajnart/self-host/internal/helpers"
)

func xteveTarget() target {
	return target{
		id: "xteve", name: "xTeVe", kind: "xteve", internal: fixed("http://xteve:34400"),
		secrets: func(_, to *helpers.H) []secret {
			user, password := to.Secret(to.Key("username")), to.Secret(to.Key("password"))
			if user == "" && password == "" && to.Secret(to.Key("auth")) == "none" {
				return []secret{}
			}
			return []secret{{Kind: "username", Value: user}, {Kind: "password", Value: password}}
		},
		widgets: []widget{statWidget("streamsAll", "streamsActive", "streamsXepg")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := xteve.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info("Preserve xTeVe authentication and its tuner, guide and playlist choices. Run self-host setup xteve for the private credential recovery instructions, then retry self-host setup homarr.")
			}
			return err
		},
		verify: verifyXteve,
		stats:  verifyXteveStats,
	}
}

func verifyXteve(ctx context.Context, _, to *helpers.H, _ string) error {
	return xteve.VerifyHomarr(ctx, to)
}

func verifyXteveStats(sample statsSample) error {
	if !validCountStats(sample, "streamsAll", "streamsActive", "streamsXepg") {
		return errors.New("Homarr returned incomplete xTeVe stream counts; retry self-host setup homarr")
	}
	if sample.values["streamsActive"].(float64) > sample.values["streamsAll"].(float64) {
		return errors.New("Homarr returned inconsistent xTeVe stream counts; retry self-host setup homarr")
	}
	return nil
}
