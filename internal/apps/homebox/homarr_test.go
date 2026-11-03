package homebox_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/homebox"
	"github.com/ajnart/self-host/internal/helpers"
)

func homarrSecrets() *homeboxSecrets {
	secrets := newHomeboxSecrets()
	secrets.values["HOMEBOX_API_KEY"] = homeboxKey
	secrets.values["HOMEBOX_ACCOUNT_IDENTITY"] = homeboxIdentity()
	return secrets
}

func TestHomeboxHomarrVerifiesSavedKeyReadOnlyAndAcceptsZeroItems(t *testing.T) {
	fixture := newHomeboxFixture()
	fixture.authorize = "Bearer " + homeboxKey
	box := newHomeboxHarness(t, fixture, homarrSecrets())
	if err := homebox.VerifyHomarr(t.Context(), box.h); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fixture.calls(), "POST /api/v1/users/login") {
		t.Fatal("Homarr verification fell back to the Admin password")
	}
	id, err := homebox.HomarrAccountID(box.h)
	if err != nil || id != homeboxAccountID {
		t.Fatalf("account id %q: %v", id, err)
	}
}

func TestHomeboxHomarrRequiresKeyAndIdentityWithoutLogin(t *testing.T) {
	for name, drop := range map[string]string{"key": "HOMEBOX_API_KEY", "identity": "HOMEBOX_ACCOUNT_IDENTITY"} {
		t.Run(name, func(t *testing.T) {
			fixture := newHomeboxFixture()
			secrets := homarrSecrets()
			delete(secrets.values, drop)
			box := newHomeboxHarness(t, fixture, secrets)
			err := homebox.VerifyHomarr(t.Context(), box.h)
			if !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("want Manual, got %v", err)
			}
			if name == "key" && !strings.Contains(err.Error(), "never issues or rotates") {
				t.Fatalf("hint lost its key boundary: %v", err)
			}
			if len(fixture.calls()) != 0 && slices.Contains(fixture.calls(), "POST /api/v1/users/login") {
				t.Fatal("Homarr verification logged in")
			}
		})
	}
}

func TestHomeboxHomarrRejectedKeyNeverFallsBackToPassword(t *testing.T) {
	fixture := newHomeboxFixture()
	fixture.authorize = "Bearer " + homeboxKey
	fixture.statuses["/api/v1/users/self"] = 401
	box := newHomeboxHarness(t, fixture, homarrSecrets())
	if err := homebox.VerifyHomarr(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("want Manual, got %v", err)
	}
	if slices.Contains(fixture.calls(), "POST /api/v1/users/login") {
		t.Fatal("rejected key fell back to the Admin password")
	}
}

func TestHomeboxHomarrRejectsDifferentOwnerOrInventory(t *testing.T) {
	fixture := newHomeboxFixture()
	fixture.authorize = "Bearer " + homeboxKey
	fixture.responses["/api/v1/users/self"] = map[string]any{"item": map[string]any{
		"id": homeboxOtherID, "email": "admin@selfhost.local", "defaultGroupId": homeboxGroupID, "groupIds": []string{homeboxGroupID},
	}}
	box := newHomeboxHarness(t, fixture, homarrSecrets())
	if err := homebox.VerifyHomarr(t.Context(), box.h); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("want Manual, got %v", err)
	}
}
