package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/romm"
	"github.com/ajnart/self-host/internal/helpers"
)

func rommTarget() target {
	return target{
		id: "romm", name: "RomM", kind: "romm", internal: (*helpers.H).InternalURL,
		secrets: func(_, _ *helpers.H) []secret { return []secret{} },
		widgets: []widget{statWidget("platforms", "roms", "saves", "states", "screenshots", "totalFileSize")},
		verify:  verifyRomm,
		stats:   verifyRommStats,
	}
}

func verifyRomm(ctx context.Context, _, to *helpers.H, _ string) error {
	return romm.VerifyStats(ctx, to)
}

func verifyRommStats(sample statsSample) error {
	if !validCountStats(sample, "platforms", "roms", "saves", "states", "screenshots") || sample.units["totalFileSize"] != "bytes" || !isCount(sample.values["totalFileSize"]) {
		return errors.New("Homarr returned incomplete RomM Stats; retry self-host setup homarr")
	}
	return nil
}
