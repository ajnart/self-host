package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type manifestEntry struct {
	Version int    `json:"version"`
	Parent  string `json:"parent"`
	Token   string `json:"token"`
	Owner   string `json:"owner"`
}

func validOwner(owner string) bool {
	_, err := hex.DecodeString(owner)
	return len(owner) == 32 && err == nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("private directory required")
	}
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) {
		return errors.New("owned directory required")
	}
	return nil
}

func openManifest(ctx context.Context, path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || privateDirectory(filepath.Dir(path)) != nil {
		return nil, errors.New("invalid manifest")
	}
	flags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		f.Close()
		return nil, errors.New("invalid manifest ownership")
	}
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return f, nil
}

func registerParent(ctx context.Context, manifest, owner string) (string, error) {
	if !validOwner(owner) {
		return "", errors.New("invalid owner")
	}
	f, err := openManifest(ctx, manifest, true)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 1024*1024 {
		return "", errors.New("manifest full")
	}
	parent, err := os.MkdirTemp(filepath.Dir(manifest), ".self-host-testkit-")
	if err != nil {
		return "", err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	entry := manifestEntry{Version: 1, Parent: filepath.Base(parent), Token: hex.EncodeToString(token[:]), Owner: owner}
	if err := os.WriteFile(filepath.Join(parent, ".bridge-owner"), []byte(entry.Token), 0600); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return "", err
	}
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		return "", err
	}
	return parent, f.Sync()
}

func cleanupManifest(ctx context.Context, manifest string, cleanup func(context.Context, string) error) error {
	return cleanupManifestOwned(ctx, manifest, "", cleanup)
}

func cleanupManifestOwned(ctx context.Context, manifest, owner string, cleanup func(context.Context, string) error) error {
	if owner != "" && !validOwner(owner) {
		return errors.New("invalid owner")
	}
	f, err := openManifest(ctx, manifest, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 1024*1024 {
		return errors.New("invalid manifest size")
	}
	scanner := bufio.NewScanner(f)
	var result error
	for scanner.Scan() {
		var entry manifestEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Version != 1 || filepath.Base(entry.Parent) != entry.Parent || !strings.HasPrefix(entry.Parent, ".self-host-testkit-") || len(entry.Token) != 64 || !validOwner(entry.Owner) {
			return errors.New("invalid manifest entry")
		}
		if _, err := hex.DecodeString(entry.Token); err != nil {
			return errors.New("invalid manifest token")
		}
		parent := filepath.Join(filepath.Dir(manifest), entry.Parent)
		if owner != "" && entry.Owner != owner {
			continue
		}
		if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if privateDirectory(parent) != nil {
			return errors.New("invalid fixture parent")
		}
		marker := filepath.Join(parent, ".bridge-owner")
		markerInfo, err := os.Lstat(marker)
		if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm()&0077 != 0 || markerInfo.Size() != 64 {
			return errors.New("invalid fixture ownership")
		}
		token, err := os.ReadFile(marker)
		if err != nil || string(token) != entry.Token {
			return errors.New("fixture ownership mismatch")
		}
		children, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		parentErr := error(nil)
		for _, child := range children {
			if child.Name() == ".bridge-owner" {
				continue
			}
			if !child.IsDir() || child.Type()&os.ModeSymlink != 0 {
				parentErr = errors.Join(parentErr, errors.New("invalid recovery child"))
				continue
			}
			root, err := filepath.EvalSymlinks(filepath.Join(parent, child.Name()))
			if err != nil {
				parentErr = errors.Join(parentErr, errors.New("recovery root resolution failed"))
				continue
			}
			parentErr = errors.Join(parentErr, cleanup(ctx, root))
		}
		if parentErr == nil {
			parentErr = os.Remove(marker)
			if parentErr == nil {
				parentErr = os.Remove(parent)
			}
		}
		result = errors.Join(result, parentErr)
	}
	return errors.Join(result, scanner.Err())
}
