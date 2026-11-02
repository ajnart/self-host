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
	for _, label := range []string{"Yes (y)", "No (n)"} {
		if !strings.Contains(flat(screen(m)), label) {
			t.Fatalf("ownership must offer a Yes/No selector\n%s", screen(m))
		}
	}
	m = drive(t, m, press("y"))
	if a.separateMediaUsers != "on" || m.onboarding.focusedKey() != whereCopyFallback {
		t.Fatal("y must choose Yes and advance to copy fallback")
	}
	m = drive(t, m, press("right"))
	if a.mediaCopyFallback != "on" {
		t.Fatal("arrow keys must choose copy fallback without typing")
	}
	m = drive(t, m, press("enter"))
	p := planFromAnswers(backend, a)
	if p.opts.Settings["DATA_ROOT"] != "/srv/data" || p.opts.Settings["MEDIA_SEPARATE_USERS"] != "on" || p.opts.Settings["MEDIA_COPY_FALLBACK"] != "on" {
		t.Fatalf("review must preserve media choices: %#v", p.opts.Settings)
	}
	if p.opts.MediaRoot != "" || p.opts.DownloadsRoot != "" {
		t.Fatal("shared storage must not pass independent legacy folders")
	}
	m = drive(t, m, press("esc"))
	for range 5 {
		m = drive(t, m, press("enter"))
	}
	if a.separateMediaUsers != "on" || m.onboarding.focusedKey() != whereMediaUsers {
		t.Fatal("returning to Where must preserve the selected answer")
	}
	m = drive(t, m, press("n"))
	if a.separateMediaUsers != "off" || m.onboarding.focusedKey() != whereCopyFallback {
		t.Fatal("n must choose No and advance")
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
	m = drive(t, m, press("right"))
	if v.input.Value() != "on" {
		t.Fatal("Settings must offer an ownership choice using arrow keys")
	}
	m = drive(t, m, press("f"))
	if v.input.Value() != "on" {
		t.Fatal("Yes/No choices must not accept arbitrary text")
	}
	m = drive(t, m, press("enter"))
	if v.edits["MEDIA_SEPARATE_USERS"] != "on" {
		t.Fatal("Yes must preserve the persisted on value")
	}
	v.beginEdit(m.ctx())
	m = drive(t, m, press("n"))
	if v.editing || v.edits["MEDIA_SEPARATE_USERS"] != "off" {
		t.Fatal("n must accept No without typing off")
	}
	v.beginEdit(m.ctx())
	m = drive(t, m, press("right"))
	m = drive(t, m, press("esc"))
	if v.editing || v.edits["MEDIA_SEPARATE_USERS"] != "off" {
		t.Fatal("Escape must cancel the selection")
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
	if !strings.Contains(out, "Select Yes/No") || !strings.Contains(out, "MEDIA_COPY_FALLBACK") || !strings.Contains(out, "[No (n)]") {
		t.Fatalf("choice hint must not scroll the selected setting out of view\n%s", out)
	}
}
