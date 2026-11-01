package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/service"
)

func TestMediaOnboardingOffersSharedStorageAndOptionalOwners(t *testing.T) {
	m, backend := freshHost(t)
	a := m.onboarding.answers
	a.fill(map[string]string{"MEDIA_LAYOUT": "shared", "DATA_ROOT": "/srv/data"})
	a.picks = picks{"jellyfin": true}
	ctx := m.ctx()
	m.onboarding.openStep(ctx, stepWhere)
	for range 4 {
		m = drive(t, m, press("enter"))
	}
	if got := m.onboarding.focusedKey(); got != whereData {
		t.Fatalf("shared storage should ask for one data folder, got %q", got)
	}
	if a.separateMediaUsers != "off" || a.mediaCopyFallback != "off" {
		t.Fatal("optional ownership and copy fallback must default off")
	}
	m = drive(t, m, press("enter"))
	if got := m.onboarding.focusedKey(); got != whereMediaUsers {
		t.Fatalf("shared storage should offer optional Linux file owners, got %q", got)
	}
	if !strings.Contains(flat(screen(m)), "not App logins") {
		t.Fatalf("Unix file ownership must be distinguished from logins\n%s", screen(m))
	}
	a.separateMediaUsers = "on"
	a.mediaCopyFallback = "on"
	p := planFromAnswers(backend, a)
	if p.opts.Settings["DATA_ROOT"] != "/srv/data" || p.opts.Settings["MEDIA_SEPARATE_USERS"] != "on" || p.opts.Settings["MEDIA_COPY_FALLBACK"] != "on" {
		t.Fatalf("review must preserve media choices: %#v", p.opts.Settings)
	}
	if p.opts.MediaRoot != "" || p.opts.DownloadsRoot != "" {
		t.Fatal("shared storage must not pass independent legacy folders")
	}
}

func TestMediaSettingsReviewAddsEditableOwnershipAndFallbackChoices(t *testing.T) {
	m, _ := settingsHost(t)
	v := m.settings
	v.apply([]service.SettingReview{
		{Key: "MEDIA_LAYOUT", Current: "legacy", Choices: []string{"legacy", "shared"}},
		{Key: "MEDIA_SEPARATE_USERS", Current: "off", Choices: []string{"off", "on"}, Note: "Linux file owners; not App logins."},
		{Key: "MEDIA_COPY_FALLBACK", Current: "off", Choices: []string{"off", "on"}},
	}, nil)
	v.cursor = slices.IndexFunc(v.rows, func(row service.SettingReview) bool { return row.Key == "MEDIA_SEPARATE_USERS" })
	if v.cursor < 0 {
		t.Fatal("Settings must expose separate media users on existing Projects")
	}
	v.beginEdit(m.ctx())
	v.cycleChoice()
	if v.input.Value() != "on" {
		t.Fatal("Settings must offer the persisted ownership choice")
	}
}

func TestMediaSettingsEditorKeepsTheSelectedRowVisibleWithItsChoiceHint(t *testing.T) {
	m, _ := settingsHost(t)
	v := m.settings
	v.apply([]service.SettingReview{
		{Key: "MEDIA_LAYOUT", Current: "legacy", Choices: []string{"legacy", "shared"}},
		{Key: "MEDIA_SEPARATE_USERS", Current: "off", Choices: []string{"off", "on"}},
		{Key: "MEDIA_COPY_FALLBACK", Current: "off", Choices: []string{"off", "on"}},
	}, nil)
	v.cursor = len(v.rows) - 1
	v.beginEdit(m.ctx())
	out := v.hostBox(m.ctx(), 80, 6)
	if !strings.Contains(out, "tab fills the next choice") || !strings.Contains(out, "MEDIA_COPY_FALLBACK") {
		t.Fatalf("choice hint must not scroll the selected setting out of view\n%s", out)
	}
}
