package homarr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestHomarrMediaOrganizerEnvelope(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(map[string]any) any
		fail   bool
	}{
		{name: "empty"},
		{name: "paginated", change: func(r map[string]any) any {
			r["missing"] = []any{map[string]any{"type": "book", "optional": nil}}
			r["missingCount"] = 100
			return []any{r}
		}},
		{name: "wrong identity", fail: true, change: func(r map[string]any) any { r["integrationId"] = "other"; return []any{r} }},
		{name: "error fallback", fail: true, change: func(r map[string]any) any { r["error"] = "private-marker"; return []any{r} }},
		{name: "false error", fail: true, change: func(r map[string]any) any { r["error"] = false; return []any{r} }},
		{name: "null array", fail: true, change: func(r map[string]any) any { r["missing"] = nil; return []any{r} }},
		{name: "missing count", fail: true, change: func(r map[string]any) any { delete(r, "queuedCount"); return []any{r} }},
		{name: "fractional count", fail: true, change: func(r map[string]any) any { r["queuedCount"] = 0.5; return []any{r} }},
		{name: "count below length", fail: true, change: func(r map[string]any) any { r["queued"] = []any{nil}; return []any{r} }},
		{name: "over page", fail: true, change: func(r map[string]any) any { r["queued"] = make([]any, 51); r["queuedCount"] = 51; return []any{r} }},
		{name: "multiple", fail: true, change: func(r map[string]any) any { return []any{r, r} }},
		{name: "object", fail: true, change: func(r map[string]any) any { return r }},
		{name: "nonobject", fail: true, change: func(map[string]any) any { return []any{nil} }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			result := map[string]any{"integrationId": "owned", "missing": []any{}, "queued": []any{}, "missingCount": 0, "queuedCount": 0}
			var value any = []any{result}
			if scenario.change != nil {
				value = scenario.change(result)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var input struct {
					JSON struct {
						IDs  []string `json:"integrationIds"`
						Page int      `json:"pageSize"`
					} `json:"json"`
				}
				if r.Method != "GET" || r.URL.Path != "/api/trpc/widget.mediaOrganizer.getData" || r.Header.Get("ApiKey") != "fixture-key" || json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil || len(input.JSON.IDs) != 1 || input.JSON.IDs[0] != "owned" || input.JSON.Page != 50 {
					t.Error("incorrect authenticated bounded request")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
			}))
			defer server.Close()
			h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			sample, err := readMediaOrganizer(t.Context(), h, "owned")
			if (err != nil) != scenario.fail || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("provider error escaped")
			}
			if err == nil && (sample.missing == nil || sample.queued == nil) {
				t.Fatal("explicit arrays lost")
			}
		})
	}
}
