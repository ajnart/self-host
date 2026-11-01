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
}
