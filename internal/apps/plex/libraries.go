package plex

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

func ReadServerLibraries(ctx context.Context, h *helpers.H) (string, map[string]string, error) {
	if !ownershipVerified(h) {
		return "", nil, &helpers.ManualError{Hint: "Verify this server in your own Plex account, record its verified_server_id through the existing Plex input, then retry self-host setup plex. No account login or claim is automated."}
	}
	token, machineID, err := savedServerToken(h)
	if err != nil {
		return "", nil, err
	}
	data, err := verifiedLibraryAccess(ctx, h, token, machineID)
	if err != nil {
		return "", nil, err
	}
	libraries := map[string]string{}
	seen := map[string]bool{}
	for _, item := range data.Directory {
		if seen[item.Key] {
			return "", nil, errors.New("Plex returned duplicate library identities; review its libraries")
		}
		seen[item.Key] = true
		if (item.Type == "movie" || item.Type == "show") && item.Agent != "com.plexapp.agents.none" {
			libraries[item.Key] = item.Type
		}
	}
	return machineID, libraries, nil
}
