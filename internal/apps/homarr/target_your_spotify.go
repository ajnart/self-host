package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/apps/yourspotify"
	"github.com/ajnart/self-host/internal/helpers"
)

func yourSpotifyTarget() target {
	return target{
		id: "your-spotify", name: "Your Spotify", kind: "yourSpotify", internal: fixed("http://your-spotify-server:8080"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("songs", "time", "artists")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := yourspotify.VerifyAccount(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info(yourspotify.AccountRecoveryHint + " Then run self-host setup your-spotify, then self-host setup homarr.")
			}
			return err
		},
		verify: func(ctx context.Context, _, to *helpers.H, _ string) error {
			return yourspotify.VerifyAccount(ctx, to)
		},
		stats: verifyYourSpotifyStats,
	}
}

func verifyYourSpotifyStats(sample statsSample) error {
	if !validMeasuredStats(sample, map[string]string{"songs": "count", "time": "seconds", "artists": "count"}) {
		return errors.New("Homarr Your Spotify Stats are incomplete or invalid; retry self-host setup homarr")
	}
	return nil
}
