package autobrr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/autobrr"
	"github.com/ajnart/self-host/internal/helpers"
)

type autobrrFaultSecrets struct {
	autobrrSecrets
	afterMemory bool
	failCapture bool
	writes      []string
}

func (s *autobrrFaultSecrets) Set(key, value string) error {
	s.writes = append(s.writes, value)
	if key == "AUTOBRR_HOMARR_KEY_PENDING" && strings.Contains(value, `"issued":{`) && s.failCapture {
		s.failCapture = false
		if s.afterMemory {
			s.autobrrSecrets[key] = value
		}
		return errors.New("unsafe-storage-diagnostic: " + value)
	}
	s.autobrrSecrets[key] = value
	return nil
}

func TestAutobrrRecoversOnlyTheCapturedKeyAfterPersistenceFailure(t *testing.T) {
	for _, afterMemory := range []bool{false, true} {
		f := &autobrrFixture{configured: true, keys: []map[string]any{}}
		s := &autobrrFaultSecrets{autobrrSecrets: autobrrAdminSecrets(), afterMemory: afterMemory, failCapture: true}
		h := autobrrH(t, f, s)
		err := (autobrr.App{}).Setup(t.Context(), h)
		if err == nil || strings.Contains(err.Error(), "issued-private-key") || strings.Contains(err.Error(), "unsafe-storage-diagnostic") || f.keyPosts != 1 {
			t.Fatalf("capture failure was hidden or leaked: %v", err)
		}
		unrelated := map[string]any{"key": "unrelated-private-key", "name": "person-owned", "scopes": []string{"existing"}, "created_at": "2026-10-01T00:00:00Z"}
		f.keys = append(f.keys, unrelated)
		f.cachedKeys = []map[string]any{unrelated}
		recovery := autobrrH(t, f, s)
		err = (autobrr.App{}).Setup(t.Context(), recovery)
		if afterMemory && err != nil || !afterMemory && !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("unexpected captured/intent-only recovery: afterMemory=%v err=%v", afterMemory, err)
		}
		if f.keyPosts != 1 || len(f.keys) != 2 || f.keys[1]["key"] != "unrelated-private-key" || f.keys[1]["name"] != "person-owned" {
			t.Fatal("recovery issued another key or changed unrelated keys")
		}
		for _, value := range s.writes {
			if strings.Contains(value, "unrelated-private-key") {
				t.Fatal("an unrelated key inventory reached private persistence")
			}
		}
		if afterMemory && (s.Get("AUTOBRR_HOMARR_API_KEY") != "issued-private-key" || s.Get("AUTOBRR_HOMARR_KEY_PENDING") != "") {
			t.Fatal("the original captured key was not finalized")
		}
		if h.Log.Redact("issued-private-key") != "[redacted]" || afterMemory && recovery.Log.Redact("unrelated-private-key") != "[redacted]" {
			t.Fatal("inventory credentials were not registered for redaction")
		}
	}
}

func TestAutobrrSavedConnectionInventoryMustContainTheExactIssuedIdentity(t *testing.T) {
	f := &autobrrFixture{configured: true, keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	h := autobrrH(t, f, s)
	if err := (autobrr.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`[]`,
		`[{"name":"self-host Homarr statistics","key":"issued-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
		`[{"name":"self-host Homarr statistics","key":"different-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
		`[{"name":"self-host Homarr statistics","key":"issued-private-key","scopes":[],"created_at":"2026-10-02T12:00:00Z"}]`,
		`[{"name":"self-host Homarr statistics","key":"issued-private-key","scopes":["unexpected"],"created_at":"2026-11-02T12:00:00Z"}]`,
		`null`,
		`[{"name":"self-host Homarr statistics","key":"issued-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
		`[{"name":"self-host Homarr statistics","key":"different-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
		`[{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"different-private-key","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
		`[{"name":"changed-name","key":"issued-private-key","scopes":[],"created_at":"2026-10-02T12:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`,
	} {
		if err := autobrr.VerifyHomarrInventory(h, []byte(body)); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("different/malformed connection identity accepted: %v", err)
		}
	}
	if err := autobrr.VerifyHomarrInventory(h, []byte(`[{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`)); err != nil {
		t.Fatalf("upstream-redacted saved credential identity was rejected: %v", err)
	}
	if err := autobrr.VerifyHomarrInventory(h, []byte(`[{"name":"person-owned","key":"unrelated-private-key","scopes":["existing"],"created_at":"2026-10-01T00:00:00Z"},{"name":"self-host Homarr statistics","key":"[REDACTED]","scopes":[],"created_at":"2026-11-02T12:00:00Z"}]`)); err != nil {
		t.Fatalf("distinct unrelated keys made an unambiguous saved credential unusable: %v", err)
	}
	f.configBody = `{"version":"v1.90.0","database":"sqlite","port":7474,"log_level":"TRACE"}`
	f.tokenRequests = 0
	if err := autobrr.VerifyHomarr(t.Context(), h); !errors.Is(err, helpers.ErrManual) || f.tokenRequests != 0 {
		t.Fatalf("unsafe logging did not hold before sending credentials: %v", err)
	}
}

func TestAutobrrDirectIdentityRejectsDuplicateCreationMetadata(t *testing.T) {
	f := &autobrrFixture{configured: true, keys: []map[string]any{}}
	s := autobrrAdminSecrets()
	if err := (autobrr.App{}).Setup(t.Context(), autobrrH(t, f, s)); err != nil {
		t.Fatal(err)
	}
	duplicate := map[string]any{"key": "different-private-key", "name": "self-host Homarr statistics", "scopes": []string{}, "created_at": "2026-11-02T12:00:00Z"}
	f.keys = append(f.keys, duplicate)
	f.cachedKeys = []map[string]any{duplicate}
	f.requests = nil
	err := autobrr.VerifyHomarr(t.Context(), autobrrH(t, f, s))
	if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "ambiguous") || f.keyPosts != 1 || len(f.keys) != 2 || s["AUTOBRR_HOMARR_API_KEY"] != "issued-private-key" {
		t.Fatalf("duplicate creation metadata was accepted or changed the credential: %v", err)
	}
	for _, request := range f.requests {
		if request == "GET /api/release/stats token" {
			t.Fatal("ambiguous direct identity reached statistics acquisition")
		}
	}
}
