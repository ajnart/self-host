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

func TestComposeEnvironmentReferencesIncludePassthroughValues(t *testing.T) {
	for _, environment := range []string{"[DERIVED, 'EXPLICIT=value']", "{DERIVED: null, EXPLICIT: value}"} {
		got, err := ComposeEnvironmentReferences([]byte("services:\n  app:\n    image: example/app\n    environment: " + environment + "\n"))
		if err != nil || !maps.Equal(got, map[string]bool{"DERIVED": false}) {
			t.Fatalf("passthrough references = %#v: %v", got, err)
		}
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
		{"duplicate dependency context", "BASE=changed\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", false},
		{"duplicate target declaration", "TOKEN=changed\nTOKEN=last\n", "TOKEN=first\nTOKEN=last\n", false},
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

func TestEnvironmentDependenciesIncludeEarlierDuplicateDeclarations(t *testing.T) {
	p := testProject(t)
	if err := p.WriteHomeFile(".env", []byte("TOKEN=${EARLIER}\nDERIVED=${TOKEN}/keep\nTOKEN=${LATER}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, err := p.ExpandEnvironmentReferences(map[string]bool{"DERIVED": true})
	if err != nil || !refs["TOKEN"] || !refs["EARLIER"] || !refs["LATER"] {
		t.Fatalf("duplicate declaration dependencies were lost: %#v, %v", refs, err)
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
	for _, tc := range []struct {
		name, current, archived string
		append                  bool
	}{
		{"new assignments", "", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n", true},
		{"new dependency", "A_TOKEN=fixture-current\n", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n", false},
		{"reversed current order", "A_TOKEN=fixture-current\nZ_BASE=fixture-current\n", "Z_BASE=fixture-base\nA_TOKEN=${Z_BASE}/token\n", false},
		{"reassigned dependency", "", "Z_BASE=fixture-first\nA_TOKEN=${Z_BASE}/token\nZ_BASE=fixture-last\n", true},
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
			want := tc.archived + "UNRELATED=keep-current\n"
			if tc.append {
				want = "UNRELATED=keep-current\n" + tc.archived
			}
			if err != nil || string(data) != want {
				t.Fatal("restoration reordered an interpolated assignment before its dependency", err)
			}
		})
	}
}

func TestRestoreEnvironmentAssignmentsPreservesRetainedDeclarationContexts(t *testing.T) {
	for _, tc := range []struct {
		name, current, archived string
		keys                    []string
		refuse                  bool
	}{
		{"retained dependent", "BASE=abc\n# keep this\nDERIVED=${BASE}/keep\n", "BASE=abc\nDERIVED=${BASE}/keep\n", []string{"BASE"}, false},
		{"retained duplicate dependency", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", []string{"TOKEN"}, false},
		{"replace between duplicate dependencies", "BASE=first\nTOKEN='current'\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", []string{"TOKEN"}, false},
		{"changed retained dependent", "BASE=current\nDERIVED=${BASE}/keep\n", "BASE=archived\nDERIVED=${BASE}/keep\n", []string{"BASE"}, true},
		{"missing earlier dependency", "BASE=last\nTOKEN=current\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", []string{"TOKEN"}, true},
		{"unavailable duplicate slot", "BASE=first\nBASE=last\n", "BASE=first\nTOKEN=${BASE}/token\nBASE=last\n", []string{"TOKEN"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, source := testProject(t), testProject(t)
			if err := current.WriteHomeFile(".env", []byte(tc.current), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := source.WriteHomeFile(".env", []byte(tc.archived), 0o600); err != nil {
				t.Fatal(err)
			}
			err := current.RestoreEnvironmentAssignments(source, tc.keys)
			if tc.refuse {
				if err == nil || !strings.Contains(err.Error(), "declaration order or interpolation dependencies") {
					t.Fatalf("ambiguous restore was not refused: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := tc.current
			if tc.name == "replace between duplicate dependencies" {
				want = tc.archived
			}
			data, err := current.ReadHomeFile(".env")
			if err != nil || string(data) != want {
				t.Fatalf("Restore changed declaration context or unrelated comments: %v", err)
			}
		})
	}
}
