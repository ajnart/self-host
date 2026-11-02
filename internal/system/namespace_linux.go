package system

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func inspectDockerNamespace(ctx context.Context, socket string) (*UserNamespace, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to the local Docker socket: %w", err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("Docker did not provide a local Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var peer *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if peerErr != nil {
		return nil, fmt.Errorf("identify the local Docker daemon: %w", peerErr)
	}
	if peer == nil || peer.Pid <= 0 {
		return nil, fmt.Errorf("the local Docker socket has no usable daemon identity")
	}
	ns := &UserNamespace{PID: int(peer.Pid), PeerUID: int(peer.Uid), PeerGID: int(peer.Gid)}
	proc, err := os.Open(filepath.Join("/proc", strconv.Itoa(ns.PID)))
	if err != nil {
		return nil, fmt.Errorf("inspect the local Docker daemon: %w", err)
	}
	defer proc.Close()
	file, err := namespaceFile(proc)
	if err != nil {
		return nil, fmt.Errorf("open the Docker user namespace: %w", err)
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return nil, err
	}
	ns.Device, ns.Inode = stat.Dev, stat.Ino
	ns.UIDMap, err = readNamespaceMap(proc, "uid_map")
	if err != nil {
		return nil, err
	}
	ns.GIDMap, err = readNamespaceMap(proc, "gid_map")
	if err != nil {
		return nil, err
	}
	cmd := namespaceCommand(ctx, file, "id", "-u")
	output, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != "0" {
		return nil, fmt.Errorf("cannot enter the local Docker user namespace as its root; nsenter from util-linux is required")
	}
	return ns, nil
}

func namespaceFile(proc *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(proc.Fd()), "ns/user", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "Docker user namespace"), nil
}

func readNamespaceMap(proc *os.File, name string) ([]IDMap, error) {
	fd, err := unix.Openat(int(proc.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("read Docker %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return nil, fmt.Errorf("could not read the Docker %s", name)
	}
	var result []IDMap
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("Docker %s contains an invalid mapping", name)
		}
		values := [3]uint64{}
		for i, field := range fields {
			values[i], err = strconv.ParseUint(field, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("Docker %s contains an invalid mapping", name)
			}
		}
		result = append(result, IDMap{Inside: values[0], Outside: values[1], Length: values[2]})
	}
	return result, nil
}

func namespaceCommand(ctx context.Context, file *os.File, args ...string) *exec.Cmd {
	args = append([]string{"--user=/proc/self/fd/3", "--"}, args...)
	cmd := exec.CommandContext(ctx, "nsenter", args...)
	cmd.ExtraFiles = []*os.File{file}
	return cmd
}

func setNamespaceMetadata(ctx context.Context, ns *UserNamespace, path string, uid, gid int, mode os.FileMode) error {
	if ns == nil || ns.PID <= 0 || !filepath.IsAbs(path) || uid < 0 || gid < 0 {
		return fmt.Errorf("invalid Docker user namespace metadata request")
	}
	proc, err := os.Open(filepath.Join("/proc", strconv.Itoa(ns.PID)))
	if err != nil {
		return fmt.Errorf("reopen the Docker user namespace: %w", err)
	}
	defer proc.Close()
	file, err := namespaceFile(proc)
	if err != nil {
		return err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Dev != ns.Device || stat.Ino != ns.Inode {
		return fmt.Errorf("the Docker user namespace changed; review the media ownership plan again")
	}
	uidMap, uidErr := readNamespaceMap(proc, "uid_map")
	gidMap, gidErr := readNamespaceMap(proc, "gid_map")
	if uidErr != nil || gidErr != nil || !slices.Equal(uidMap, ns.UIDMap) || !slices.Equal(gidMap, ns.GIDMap) {
		return fmt.Errorf("the Docker identity mappings changed; review the media ownership plan again")
	}
	permissions := uint32(mode.Perm())
	if mode&os.ModeSetgid != 0 {
		permissions |= 02000
	}
	if mode&os.ModeSetuid != 0 {
		permissions |= 04000
	}
	if mode&os.ModeSticky != 0 {
		permissions |= 01000
	}
	// Positional arguments keep paths out of shell source; no recursive change
	// is permitted. The pinned namespace FD prevents PID reuse during execution.
	cmd := namespaceCommand(ctx, file, "sh", "-c", `chown -- "$1:$2" "$4" && chmod -- "$3" "$4"`, "self-host-media", strconv.Itoa(uid), strconv.Itoa(gid), strconv.FormatUint(uint64(permissions), 8), path)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set newly created media directory ownership in the Docker user namespace: %w", err)
	}
	return nil
}
