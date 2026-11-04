package romm

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ajnart/self-host/internal/helpers"
)

const statsHint = "Run self-host setup romm to confirm RomM is reachable with authentication enabled, then retry self-host setup homarr. Do not disable authentication, import ROMs or expose assets to finish the link."

func VerifyStats(ctx context.Context, h *helpers.H) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/stats", NoRedirect: true})
	if err != nil {
		return err
	}
	if response.Status != http.StatusOK {
		return &helpers.ManualError{Hint: fmt.Sprintf("RomM Stats returned HTTP %d. %s", response.Status, statsHint)}
	}
	var stats struct {
		Platforms   *int64 `json:"PLATFORMS"`
		Roms        *int64 `json:"ROMS"`
		Saves       *int64 `json:"SAVES"`
		States      *int64 `json:"STATES"`
		Screenshots *int64 `json:"SCREENSHOTS"`
		TotalBytes  *int64 `json:"TOTAL_FILESIZE_BYTES"`
	}
	if err := response.Decode(&stats); err != nil {
		return &helpers.ManualError{Hint: "RomM did not return readable Stats. " + statsHint}
	}
	for _, value := range []*int64{stats.Platforms, stats.Roms, stats.Saves, stats.States, stats.Screenshots, stats.TotalBytes} {
		if value == nil || *value < 0 {
			return errors.New("RomM returned incomplete Stats; retry self-host setup romm")
		}
	}
	return nil
}
