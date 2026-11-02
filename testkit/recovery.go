package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ajnart/self-host/internal/project"
	"golang.org/x/sys/unix"
)

const markerName = ".self-host-testkit.json"

type ownership struct {
	Version  int    `json:"version"`
	Root     string `json:"root"`
	Name     string `json:"name"`
	App      string `json:"app"`
	Endpoint string `json:"endpoint"`
	PID      int    `json:"pid"`
}

func writeOwnership(marker ownership) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(marker.Root, markerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	if err != nil {
		return err
	}
	return f.Sync()
}

func privateOwned(path string, directory bool) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 || info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func readOwnership(root string) (ownership, error) {
	var marker ownership
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) || resolved != filepath.Clean(root) || !strings.HasPrefix(filepath.Base(root), "selfhost-testkit-") || !privateOwned(root, true) || !privateOwned(filepath.Join(root, markerName), false) {
		return marker, errors.New("testkit refuses unowned recovery root")
	}
	data, err := os.ReadFile(filepath.Join(root, markerName))
	if err != nil || json.Unmarshal(data, &marker) != nil || marker.Version != 1 || marker.Root != resolved || !regexp.MustCompile(`^testkit-[a-f0-9]{24}$`).MatchString(marker.Name) || !strings.HasPrefix(marker.Endpoint, "unix:///") || marker.PID <= 0 {
		return ownership{}, errors.New("testkit refuses invalid private ownership marker")
	}
	return marker, nil
}

func Cleanup(ctx context.Context, root string) error {
	marker, err := readOwnership(root)
	if err != nil {
		return err
	}
	if err := unix.Kill(marker.PID, 0); err == nil || err == unix.EPERM {
		return errors.New("testkit helper is still alive; close its App first")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	unlock, err := startupLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	env := fixtureEnvironment(environmentSnapshot(), marker.Endpoint)
	runner := environmentRunner{env: environmentList(env)}
	if err := inactive(ctx, runner, marker.Name, true); err != nil {
		return err
	}
	if _, err := readOwnership(root); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func decodeDigests(data []byte, target *[]string) error {
	if json.Unmarshal(data, target) != nil {
		return errors.New("testkit image provenance unavailable")
	}
	for _, digest := range *target {
		if !project.ValidFixtureImage(digest) || !strings.Contains(digest, "@sha256:") {
			return errors.New("testkit image provenance invalid")
		}
	}
	return nil
}
