package skill

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var router []byte

const (
	entryName     = "self-host"
	markerName    = ".self-host-managed"
	markerContent = "written by self-host skill install\n"
)

const (
	Written    = "written"
	UpToDate   = "up to date"
	LeftAlone  = "left alone, managed elsewhere"
	Skipped    = "skipped, agent not installed"
	Removed    = "removed"
	NotPresent = "not installed"
)

// Result is what happened at one agent skills directory.
type Result struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

type target struct {
	agent  string
	home   string
	skills string
	always bool
}

func targets(home string) []target {
	list := []target{
		{"generic", filepath.Join(home, ".agents"), filepath.Join(home, ".agents", "skills"), true},
		{"claude", filepath.Join(home, ".claude"), filepath.Join(home, ".claude", "skills"), false},
		{"codex", filepath.Join(home, ".codex"), filepath.Join(home, ".codex", "skills"), false},
		{"pi", filepath.Join(home, ".pi", "agent"), filepath.Join(home, ".pi", "agent", "skills"), false},
		{"gemini", filepath.Join(home, ".gemini"), filepath.Join(home, ".gemini", "skills"), false},
		{"hermes", filepath.Join(home, ".hermes"), filepath.Join(home, ".hermes", "skills"), false},
		{"opencode", filepath.Join(home, ".config", "opencode"), filepath.Join(home, ".config", "opencode", "skills"), false},
	}
	profiles, _ := filepath.Glob(filepath.Join(home, ".hermes", "profiles", "*"))
	for _, profile := range profiles {
		if info, err := os.Stat(profile); err == nil && info.IsDir() {
			list = append(list, target{"hermes", profile, filepath.Join(profile, "skills"), false})
		}
	}
	return list
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ours reports whether dir is a real directory that self-host wrote.
func ours(dir string) (exists, managed bool, err error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return true, false, nil
	}
	for _, name := range []string{markerName, "SKILL.md"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			if name == markerName {
				return true, false, nil
			}
			continue
		}
		if err != nil {
			return true, false, err
		}
		if !info.Mode().IsRegular() {
			return true, false, nil
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return true, false, err
	}
	return true, string(marker) == markerContent, nil
}

func linkedParent(home, dir string) (bool, error) {
	for parent := filepath.Dir(dir); parent != filepath.Clean(home); parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return true, nil
		}
		if filepath.Dir(parent) == parent {
			return false, fmt.Errorf("skill directory is outside home")
		}
	}
	return false, nil
}

// Install writes the router skill into every agent skills directory whose
// agent home exists under home, and into ~/.agents/skills always.
func Install(home string) ([]Result, error) {
	var results []Result
	var errs []error
	for _, t := range targets(home) {
		dir := filepath.Join(t.skills, entryName)
		linked, err := linkedParent(home, dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		if linked {
			results = append(results, Result{t.agent, dir, LeftAlone})
			continue
		}
		if !t.always && !isDir(t.home) {
			results = append(results, Result{t.agent, dir, Skipped})
			continue
		}
		status, err := installOne(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		results = append(results, Result{t.agent, dir, status})
	}
	return results, errors.Join(errs...)
}

func installOne(dir string) (string, error) {
	exists, managed, err := ours(dir)
	if err != nil {
		return "", err
	}
	if exists && !managed {
		return LeftAlone, nil
	}
	file := filepath.Join(dir, "SKILL.md")
	if current, err := os.ReadFile(file); err == nil && managed && bytes.Equal(current, router) {
		return UpToDate, nil
	}
	if !exists {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		if err := os.Mkdir(dir, 0o755); errors.Is(err, fs.ErrExist) {
			return LeftAlone, nil
		} else if err != nil {
			return "", err
		}
	}
	if err := replaceFile(file, router); err != nil {
		return "", err
	}
	if err := replaceFile(filepath.Join(dir, markerName), []byte(markerContent)); err != nil {
		return "", err
	}
	return Written, nil
}

func replaceFile(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".self-host-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Chmod(0o644); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// Remove deletes only the directories Install wrote.
func Remove(home string) ([]Result, error) {
	var results []Result
	var errs []error
	list := append(targets(home), target{"gemini", filepath.Join(home, ".gemini"), filepath.Join(home, ".gemini", "config", "skills"), false})
	for _, t := range list {
		dir := filepath.Join(t.skills, entryName)
		linked, err := linkedParent(home, dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		if linked {
			results = append(results, Result{t.agent, dir, LeftAlone})
			continue
		}
		exists, managed, err := ours(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		switch {
		case !exists:
			results = append(results, Result{t.agent, dir, NotPresent})
		case !managed:
			results = append(results, Result{t.agent, dir, LeftAlone})
		default:
			if err := removeOurs(dir); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", dir, err))
				continue
			}
			results = append(results, Result{t.agent, dir, Removed})
		}
	}
	return results, errors.Join(errs...)
}

func removeOurs(dir string) error {
	for _, name := range []string{"SKILL.md", markerName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	return os.Remove(dir)
}

// Router returns the content written as SKILL.md.
func Router() []byte { return bytes.Clone(router) }
