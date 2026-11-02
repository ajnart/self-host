package project

import (
	"maps"
	"strings"
	"testing"
)

func TestComposeEnvironmentReferencesRespectDefaultsAndEscapes(t *testing.T) {
	got, err := ComposeEnvironmentReferences([]byte("services:\n  app:\n    image: example/app\n    environment:\n      TOKEN: ${MANUAL_TOKEN}\n      OPTIONAL: ${OPTIONAL:-default}\n      PLAIN: $PLAIN\n      LITERAL: $$NOT_A_PROJECT_KEY\n      ${NOT_INTERPOLATED_KEY}: value\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"MANUAL_TOKEN": true, "OPTIONAL": false, "PLAIN": true}
	if !maps.Equal(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
}

func TestComposeEnvironmentReferencesIncludeResourceEnvironmentSources(t *testing.T) {
	got, err := ComposeEnvironmentReferences([]byte(`services:
  app:
    image: example/app
    environment:
      TOKEN: ${RESOURCE_TOKEN:-optional}
      LITERAL: ordinary-value
    secrets: [token]
    configs: [configuration]
secrets:
  token:
    environment: RESOURCE_TOKEN
  file-secret:
    file: ./private.txt
configs:
  configuration:
    environment: RESOURCE_CONFIG
  inline:
    content: literal-content
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"RESOURCE_TOKEN": true, "RESOURCE_CONFIG": true}
	if !maps.Equal(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
}

func TestEnvironmentAssignmentMatchesPreservesQuotingSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, current, archived string
		match                   bool
	}{
		{"same interpolation", "TOKEN=${BASE}\n", "TOKEN=${BASE}\n", true},
		{"literal versus interpolation", "TOKEN=${BASE}\n", "TOKEN='${BASE}'\n", false},
		{"double versus single quotes", "TOKEN=\"${BASE}\"\n", "TOKEN='${BASE}'\n", false},
		{"conservative quote comparison", "TOKEN=fixture-value\n", "TOKEN='fixture-value'\n", false},
		{"assignment prefix", "export TOKEN = ${BASE}\n", "TOKEN=${BASE}\n", true},
		{"different value", "TOKEN=fixture-current\n", "TOKEN=fixture-archived\n", false},
		{"absent versus empty", "UNRELATED=fixture\n", "TOKEN=\n", false},
		{"both absent", "UNRELATED=fixture\n", "OTHER=fixture\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := &Project{Root: t.TempDir()}
			archived := &Project{Root: t.TempDir()}
			if err := current.WriteHomeFile(".env", []byte(tc.current), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := archived.WriteHomeFile(".env", []byte(tc.archived), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := current.EnvironmentAssignmentMatches(archived, "TOKEN")
			if err != nil || got != tc.match {
				t.Fatalf("assignment match = %t, want %t: %v", got, tc.match, err)
			}
		})
	}
}

func TestRestoreEnvironmentAssignmentsKeepInterpolationAndDependencies(t *testing.T) {
	source := &Project{Root: t.TempDir()}
	current := &Project{Root: t.TempDir()}
	if err := source.WriteHomeFile(".env", []byte("TOKEN=\"${BASE}\" # preserve interpolation\nBASE=fixture-old\nLITERAL='${NOT_A_DEPENDENCY}'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := current.WriteHomeFile(".env", []byte("TOKEN=fixture-new\nBASE=fixture-new\nUNRELATED=keep-current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err := source.ExpandEnvironmentReferences(map[string]bool{"TOKEN": true, "LITERAL": true})
	if err != nil || !refs["BASE"] {
		t.Fatalf("missing interpolated environment dependency: %v", err)
	}
	if _, found := refs["NOT_A_DEPENDENCY"]; found {
		t.Fatal("single-quoted environment assignments must remain literal")
	}
	if err := current.RestoreEnvironmentAssignments(source, []string{"TOKEN", "BASE"}); err != nil {
		t.Fatal(err)
	}
	data, err := current.ReadHomeFile(".env")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TOKEN=\"${BASE}\" # preserve interpolation", "BASE=fixture-old", "UNRELATED=keep-current"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("restoration changed interpolation syntax or unrelated assignments")
		}
	}
	for _, tc := range []struct{ name, current, archived string }{
		{"new assignments", "", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n"},
		{"new dependency", "A_TOKEN=fixture-current\n", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n"},
		{"reversed current order", "A_TOKEN=fixture-current\nZ_BASE=fixture-current\n", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n"},
		{"reassigned dependency", "", "Z_BASE=fixture-first\nA_TOKEN=${Z_BASE}/token\nZ_BASE=fixture-last\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, current := testProject(t), testProject(t)
			if err := source.WriteHomeFile(".env", []byte(tc.archived), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := current.WriteHomeFile(".env", []byte(tc.current+"UNRELATED=keep-current\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := current.RestoreEnvironmentAssignments(source, []string{"A_TOKEN", "Z_BASE"}); err != nil {
				t.Fatal(err)
			}
			data, err := current.ReadHomeFile(".env")
			if err != nil || string(data) != "UNRELATED=keep-current\n"+tc.archived {
				t.Fatal("restoration reordered an interpolated assignment before its dependency", err)
			}
		})
	}
}
