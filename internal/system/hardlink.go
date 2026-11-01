package system

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// probeHardlink never creates the selected folders or touches existing files.
func probeHardlink(sourceDir, targetDir string) (result error) {
	source, err := os.CreateTemp(sourceDir, ".self-host-hardlink-")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.Remove(source.Name())) }()
	if err := source.Close(); err != nil {
		return err
	}
	target := filepath.Join(targetDir, ".self-host-link-"+filepath.Base(source.Name()))
	if err := os.Link(source.Name(), target); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.Remove(target)) }()
	before, err := os.Stat(source.Name())
	if err != nil {
		return err
	}
	after, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("temporary link does not refer to the same file")
	}
	return nil
}
