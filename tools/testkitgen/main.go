package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/ajnart/self-host/testkit"
)

func main() {
	check := flag.Bool("check", false, "check generated startup reasons without writing")
	flag.Parse()
	if err := generate(*check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(check bool) error {
	hints := testkit.StartupReasons()
	reasons := make([]testkit.StartupReason, 0, len(hints))
	for reason := range hints {
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)
	var output bytes.Buffer
	output.WriteString("export const startupReasons = {\n")
	for _, reason := range reasons {
		line := fmt.Sprintf("\t%s: %q,\n", reason, hints[reason])
		if len(line) > 80 {
			line = fmt.Sprintf("\t%s:\n\t\t%q,\n", reason, hints[reason])
		}
		output.WriteString(line)
	}
	output.WriteString("} as const;\n\nexport type TestkitStartupReason = keyof typeof startupReasons;\n")
	const path = "testkit/node/src/reasons.mts"
	if check {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read generated startup reasons: %w", err)
		}
		if !bytes.Equal(current, output.Bytes()) {
			return errors.New("startup reasons are stale; run go run ./tools/testkitgen")
		}
		return nil
	}
	if err := os.WriteFile(path, output.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write generated startup reasons: %w", err)
	}
	return nil
}
