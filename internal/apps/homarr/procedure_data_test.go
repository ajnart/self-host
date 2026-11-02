package homarr

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestHomarrProcedureUndefinedWirePaths(t *testing.T) {
	wire := `{"result":{"data":{"json":{"absent":null,"nullable":null,"nested":{"absent":null,"kept":0},"list":[null,null,{"absent":null,"kept":true}],"literal.dot":{"back\\slash":null},"":{"":null},"numeric":{"0":null,"01":null},"date":"2026-10-13T00:00:00.000Z"},"meta":{"values":{"absent":["undefined"],"nested.absent":["undefined"],"list.0":["undefined"],"list.2.absent":["undefined"],"literal\\.dot.back\\\\slash":["undefined"],".":["undefined"],"numeric.0":["undefined"],"date":["Date"]},"v":1}}}}`
	value, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(wire)})
	if err != nil {
		t.Fatal(err)
	}
	record, ok := value.(map[string]any)
	if !ok {
		t.Fatal("procedure object missing")
	}
	for _, key := range []string{"omitted", "absent"} {
		if _, present := record[key]; present {
			t.Fatalf("optional field %s remained present", key)
		}
	}
	if nullable, present := record["nullable"]; !present || nullable != nil {
		t.Fatal("genuine null changed")
	}
	for _, key := range []string{"literal.dot", ""} {
		if nested, ok := record[key].(map[string]any); !ok || len(nested) != 0 {
			t.Fatal("escaped or empty property path was not normalized")
		}
	}
	if !reflect.DeepEqual(record["nested"], map[string]any{"kept": float64(0)}) || !reflect.DeepEqual(record["numeric"], map[string]any{"01": nil}) {
		t.Fatal("nested or numeric object keys changed")
	}
	list, ok := record["list"].([]any)
	if !ok || len(list) != 3 || list[0] == nil || list[1] != nil || !reflect.DeepEqual(list[2], map[string]any{"kept": true}) {
		t.Fatal("undefined array slot collapsed or shifted")
	}
	if record["date"] != "2026-10-13T00:00:00.000Z" {
		t.Fatal("date representation changed")
	}
}

func TestHomarrProcedureUndefinedAnnotationTrees(t *testing.T) {
	for _, scenario := range []struct {
		name string
		data string
		want any
	}{
		{name: "nested transformed tree", data: `{"json":{"set":[{"absent":null,"nullable":null}]},"meta":{"values":{"set":["set",{"0.absent":["undefined"]}]},"v":1}}`, want: map[string]any{"set": []any{map[string]any{"nullable": nil}}}},
		{name: "nested plain tree", data: `{"json":{"items":[{"optional":null,"value":0}]},"meta":{"values":{"items":{"0":{"optional":["undefined"]}}},"v":1}}`, want: map[string]any{"items": []any{map[string]any{"value": float64(0)}}}},
		{name: "legacy path escaping", data: `{"json":{"literal.dot":{"back\\slash":null}},"meta":{"values":{"literal\\.dot.back\\slash":["undefined"]}}}`, want: map[string]any{"literal.dot": map[string]any{}}},
		{name: "legacy explicit version", data: `{"json":{"literal.dot":{"back\\slash":null}},"meta":{"values":{"literal\\.dot.back\\slash":["undefined"]},"v":0}}`, want: map[string]any{"literal.dot": map[string]any{}}},
		{name: "genuine null", data: `{"json":null}`, want: nil},
		{name: "omitted result", data: `{}`, want: nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			value, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(`{"result":{"data":` + scenario.data + `}}`)})
			if err != nil || !reflect.DeepEqual(value, scenario.want) {
				t.Fatalf("incorrect normalized data: error=%v", err)
			}
		})
	}
	value, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(`{"result":{"data":{"json":null,"meta":{"values":["undefined"],"v":1}}}}`)})
	if err != nil || value == nil {
		t.Fatal("root undefined collapsed into genuine null", err)
	}
}

func TestHomarrProcedureUndefinedRejectsInconsistentAnnotations(t *testing.T) {
	for _, data := range []string{
		`{"json":{"private-test-password":"private-test-password"},"meta":{"values":{"private-test-password":["undefined"]},"v":1}}`,
		`{"json":{},"meta":{"values":{"absent":["undefined"]},"v":1}}`,
		`{"json":{"parent":null},"meta":{"values":{"parent.child":["undefined"]},"v":1}}`,
		`{"json":[null],"meta":{"values":{"1":["undefined"]},"v":1}}`,
		`{"json":[null],"meta":{"values":{"-1":["undefined"]},"v":1}}`,
		`{"json":[null],"meta":{"values":{"01":["undefined"]},"v":1}}`,
		`{"json":{"bad\\path":null},"meta":{"values":{"bad\\path":["undefined"]},"v":1}}`,
		`{"json":{"bad\\":null},"meta":{"values":{"bad\\":["undefined"]},"v":1}}`,
		`{"json":{"constructor":null},"meta":{"values":{"constructor":["undefined"]},"v":1}}`,
		`{"json":{"__proto__":null},"meta":{"values":{"__proto__":["undefined"]},"v":1}}`,
		`{"json":{"prototype":null},"meta":{"values":{"prototype":["undefined"]},"v":1}}`,
		`{"json":{"field":null},"meta":{"values":{"field":["undefined",{"child":["undefined"]}]},"v":1}}`,
		`{"json":{"field":null},"meta":{"values":{"field":"undefined"},"v":1}}`,
		`{"json":{"field":null},"meta":{"values":{"field":[]},"v":1}}`,
		`{"json":{"field":null},"meta":{"values":{"field":["undefined",null]},"v":1}}`,
		`{"json":{"parent":{"field":null}},"meta":{"values":{"parent.field":["undefined"],"parent":{"field":["undefined"]}},"v":1}}`,
		`{"meta":{"values":["undefined"],"v":1}}`,
	} {
		_, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(`{"result":{"data":` + data + `}}`)})
		if err == nil || len(err.Error()) > 128 || strings.Contains(err.Error(), "private-test-password") {
			t.Fatal("inconsistent annotation did not fail safely", err)
		}
	}
}

func TestHomarrProcedureOtherMetadataRemainsUninterpreted(t *testing.T) {
	json := `{"date":"2026-10-13T00:00:00.000Z","updatedAt":1791849600000,"retryAt":null,"values":{"count":0},"number":"NaN","bigint":"123","alias":null}`
	plain, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(`{"result":{"data":{"json":` + json + `}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	annotated, err := decodeProcedure(helpers.Response{Status: 200, Body: []byte(`{"result":{"data":{"json":` + json + `,"meta":{"values":{"date":["Date"],"number":["number"],"bigint":["bigint"]},"referentialEqualities":{"values":["alias"]},"v":1}}}}`)})
	if err != nil || !reflect.DeepEqual(plain, annotated) {
		t.Fatal("unrelated metadata changed existing data representations", err)
	}
}

func TestHomarrProcedureReadersShareUndefinedNormalization(t *testing.T) {
	for _, scenario := range []struct {
		name string
		read func(*helpers.H) (any, error)
		key  bool
	}{
		{name: "session query", read: func(h *helpers.H) (any, error) { return newSession(h).query(t.Context(), h, "fixture.read", nil) }},
		{name: "session mutation", read: func(h *helpers.H) (any, error) {
			return newSession(h).mutation(t.Context(), h, "fixture.write", map[string]any{})
		}},
		{name: "API key query", key: true, read: func(h *helpers.H) (any, error) { return apiKeyProcedure(t.Context(), h, "fixture.read", nil) }},
		{name: "API key query with input", key: true, read: func(h *helpers.H) (any, error) {
			return apiKeyProcedure(t.Context(), h, "fixture.read", map[string]any{"id": "synthetic"})
		}},
		{name: "API key mutation", key: true, read: func(h *helpers.H) (any, error) { return apiKeyWrite(t.Context(), h, "fixture.write", map[string]any{}) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (r.Header.Get("ApiKey") == "fixture-key") != scenario.key {
					t.Error("credential selection changed")
				}
				w.Write([]byte(`{"result":{"data":{"json":{"optional":null,"nullable":null},"meta":{"values":{"optional":["undefined"]},"v":1}}}}`))
			}))
			defer server.Close()
			h := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, URL: server.URL, HTTP: server.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"}})
			value, err := scenario.read(h)
			if err != nil || !reflect.DeepEqual(value, map[string]any{"nullable": nil}) {
				t.Fatal("reader did not reuse common undefined normalization", err)
			}
		})
	}
}
