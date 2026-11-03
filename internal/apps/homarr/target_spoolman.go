package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/spoolman"
	"github.com/ajnart/self-host/internal/helpers"
)

func spoolmanTarget() target {
	return target{
		id: "spoolman", name: "Spoolman", kind: "spoolman", internal: (*helpers.H).InternalURL,
		widgets: []widget{statWidget("spools")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			return verifySpoolman(ctx, from, to, "")
		},
		verify: verifySpoolman,
		stats:  verifySpoolmanStats,
	}
}

func verifySpoolman(ctx context.Context, from, to *helpers.H, _ string) error {
	err := spoolman.VerifyInventory(ctx, to)
	if errors.Is(err, helpers.ErrManual) {
		from.Info("Preserve Spoolman's existing authentication, database, credentials and inventory. Follow self-host setup spoolman recovery, then retry self-host setup homarr; no Homarr mutation is permitted while that prerequisite is Manual.")
	}
	return err
}

func verifySpoolmanStats(sample statsSample) error {
	if !validCountStats(sample, "spools") {
		return errors.New("Homarr returned invalid Spoolman spool-count statistics; retry self-host setup homarr")
	}
	if retryAt, ok := countValue(sample.retryAt); !ok || retryAt != 0 {
		return errors.New("Homarr did not complete a successful Spoolman Stats refresh; retry self-host setup homarr")
	}
	return nil
}
