package trilium

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type setupStatus struct {
	IsInitialized        *bool           `json:"isInitialized"`
	SchemaExists         *bool           `json:"schemaExists"`
	AuthRequired         *bool           `json:"authRequired"`
	SecondFactorRequired *bool           `json:"secondFactorRequired"`
	HasExistingData      *bool           `json:"hasExistingData"`
	SetupOperation       json.RawMessage `json:"setupOperation"`
	SyncVersion          *int            `json:"syncVersion"`
}

type bootstrap struct {
	DBInitialized *bool `json:"dbInitialized"`
	PasswordSet   *bool `json:"passwordSet"`
	LoggedIn      *bool `json:"loggedIn"`
	Login         *struct {
		SSOEnabled  *bool `json:"ssoEnabled"`
		TOTPEnabled *bool `json:"totpEnabled"`
	} `json:"login"`
}

func loginManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + loginRecovery}
}

func verifyLogin(ctx context.Context, h *helpers.H) error {
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/setup/status", NoRedirect: true})
	if err != nil {
		return errors.New("Trilium setup status could not be read; retry self-host setup trilium")
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Trilium setup status returned HTTP %d; review its Address and selected image", resp.Status)
	}
	var state setupStatus
	if resp.Decode(&state) != nil || state.IsInitialized == nil || state.SchemaExists == nil || state.AuthRequired == nil || state.SecondFactorRequired == nil || state.HasExistingData == nil || state.SyncVersion == nil || *state.SyncVersion <= 0 || len(state.SetupOperation) == 0 {
		return loginManual("Trilium did not return a complete setup state; no bootstrap was attempted.")
	}
	if !*state.IsInitialized || !*state.SchemaExists || *state.HasExistingData || *state.AuthRequired || *state.SecondFactorRequired || !bytes.Equal(bytes.TrimSpace(state.SetupOperation), []byte("null")) {
		return loginManual("Trilium needs a notebook selection, setup completion or recovery; existing data was left untouched.")
	}
	password := h.AdminPassword()
	if password == "" {
		return loginManual("The Project's Admin password is missing; no sign-in was attempted.")
	}
	h.Log.Secret(password)
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/app-info", NoRedirect: true})
	if err != nil {
		return errors.New("Trilium authentication requirements could not be checked")
	}
	if resp.Status != http.StatusUnauthorized {
		return loginManual("Trilium did not require the expected password-authenticated session; review its authentication without weakening it.")
	}
	session := h.Session()
	before, err := readBootstrap(ctx, session)
	if err != nil {
		return err
	}
	if before.DBInitialized == nil || !*before.DBInitialized || before.LoggedIn == nil || *before.LoggedIn || before.Login == nil || before.Login.SSOEnabled == nil || before.Login.TOTPEnabled == nil {
		return loginManual("Trilium did not expose its expected existing-password login state.")
	}
	if *before.Login.SSOEnabled || *before.Login.TOTPEnabled {
		return loginManual("Trilium uses SSO or a second factor; keep those settings and complete sign-in yourself.")
	}
	resp, err = session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/login", NoRedirect: true,
		Form: url.Values{"password": {password}},
	})
	if err != nil {
		return errors.New("Trilium sign-in could not be completed; no password or token was changed")
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusTooManyRequests {
		return loginManual("Trilium rejected or rate-limited the saved password; preserve the credential and review sign-in before retrying.")
	}
	if resp.Status != http.StatusFound || resp.Header.Get("Location") != "." {
		return loginManual("Trilium did not confirm the expected local password sign-in; no redirect was followed.")
	}
	after, err := readBootstrap(ctx, session)
	if err != nil {
		return err
	}
	if after.DBInitialized == nil || !*after.DBInitialized || after.PasswordSet == nil || !*after.PasswordSet || after.LoggedIn == nil || !*after.LoggedIn {
		return loginManual("Trilium did not confirm an initialized, password-protected authenticated session.")
	}
	return verifyApplication(ctx, session, *state.SyncVersion)
}

func readBootstrap(ctx context.Context, session *helpers.Session) (bootstrap, error) {
	resp, err := session.HTTP(ctx, helpers.Request{Path: "/bootstrap", NoRedirect: true})
	if err != nil {
		return bootstrap{}, errors.New("Trilium login state could not be read")
	}
	var state bootstrap
	if resp.Status != http.StatusOK || resp.Decode(&state) != nil {
		return bootstrap{}, loginManual("Trilium did not return a readable login state.")
	}
	return state, nil
}

func verifyApplication(ctx context.Context, session *helpers.Session, syncVersion int) error {
	resp, err := session.HTTP(ctx, helpers.Request{Path: "/api/app-info", NoRedirect: true})
	if err != nil {
		return errors.New("Trilium authenticated application information could not be read")
	}
	var info struct {
		AppVersion     string `json:"appVersion"`
		DBVersion      *int   `json:"dbVersion"`
		SyncVersion    *int   `json:"syncVersion"`
		BuildDate      string `json:"buildDate"`
		BuildRevision  string `json:"buildRevision"`
		ClipperVersion string `json:"clipperProtocolVersion"`
		UTCDateTime    string `json:"utcDateTime"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&info) != nil || !helpers.VersionCompatible(info.AppVersion, "0.106.1") || info.DBVersion == nil || *info.DBVersion <= 0 || info.SyncVersion == nil || *info.SyncVersion != syncVersion || strings.TrimSpace(info.BuildRevision) == "" || info.ClipperVersion != "1.0" {
		return loginManual("Trilium did not return compatible authenticated application and sync information.")
	}
	if _, err := time.Parse(time.RFC3339Nano, info.BuildDate); err != nil {
		return loginManual("Trilium did not return a valid application build date.")
	}
	if _, err := time.Parse(time.RFC3339Nano, info.UTCDateTime); err != nil {
		return loginManual("Trilium did not return a valid application start timestamp.")
	}
	return nil
}
