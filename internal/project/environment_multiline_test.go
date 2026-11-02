package project

import (
	"strings"
	"testing"
)

func TestEnvironmentQuotedValueDecoding(t *testing.T) {
	for _, tc := range []struct{ name, assignment, value string }{
		{"double multiline", "TOKEN=\"first\nsecond\" # note\n", "first\nsecond"},
		{"escaped newline", "TOKEN=\"first\\nsecond\"\n", "first\nsecond"},
		{"single literal", "TOKEN='${LITERAL}\\n'\n", "${LITERAL}\\n"},
		{"escaped apostrophe", "TOKEN='It\\'s a\nfixture'\n", "It's a\nfixture"},
		{"literal backslashes", "TOKEN='C:\\fixture\\last'\n", "C:\\fixture\\last"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseEnv([]byte(tc.assignment)); len(got) != 1 || got["TOKEN"] != tc.value {
				t.Fatal("quoted environment value was not decoded correctly")
			}
		})
	}
}

func TestQuotedMultilineEnvironmentPreservesWholeAssignments(t *testing.T) {
	p := testProject(t)
	block := "TOKEN='first fixture line\nUMASK=inside-the-private-value\nlast fixture line' # keep this comment\n"
	initial := "# keep manual values\n" + block + "TZ=UTC\n"
	if err := p.WriteHomeFile(".env", []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	secrets, err := p.LoadSecrets()
	if err != nil || secrets["TOKEN"] != "first fixture line\nUMASK=inside-the-private-value\nlast fixture line" {
		t.Fatal("multiline private value was truncated", err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := settings["UMASK"]; exists {
		t.Fatal("a line inside a private value became a public Host setting")
	}
	settings["TZ"] = "Europe/Paris"
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSecrets(secrets); err != nil {
		t.Fatal(err)
	}
	data, _ := p.ReadHomeFile(".env")
	if !strings.Contains(string(data), block) {
		t.Fatal("settings or secret writes changed the quoted block")
	}
	delete(secrets, "TOKEN")
	if err := p.SaveSecrets(secrets); err != nil {
		t.Fatal(err)
	}
	data, _ = p.ReadHomeFile(".env")
	if strings.Contains(string(data), "fixture line") || strings.Contains(string(data), "inside-the-private-value") {
		t.Fatal("removing a private assignment left its continuation lines")
	}
}

func TestRestoreEnvironmentAssignmentsReplacesMultilineBlocks(t *testing.T) {
	source, current := testProject(t), testProject(t)
	archived := "TOKEN='archived first\narchived last' # preserved\n"
	if err := source.WriteHomeFile(".env", []byte(archived), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := "UNRELATED=\"keep first\nkeep last\"\n"
	if err := current.WriteHomeFile(".env", []byte("TOKEN='current first\ncurrent last'\n"+unrelated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := current.RestoreEnvironmentAssignments(source, []string{"TOKEN"}); err != nil {
		t.Fatal(err)
	}
	data, _ := current.ReadHomeFile(".env")
	if string(data) != unrelated+archived {
		t.Fatal("multiline restoration lost the archived block or unrelated assignment")
	}
}
