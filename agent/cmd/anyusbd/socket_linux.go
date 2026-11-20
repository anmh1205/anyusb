package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func listenLocal(path string) (*net.UnixListener, *os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, fmt.Errorf("socket requires a clean, absolute path")
	}
	if err := ensureSocketDirectory(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("open agent socket lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("lock %s: %w (is anyusbd already running?)", path, err)
	}
	if err := removeStaleSocket(path); err != nil {
		lock.Close()
		return nil, nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		lock.Close()
		return nil, nil, fmt.Errorf("set private socket permissions: %w", err)
	}
	return listener, lock, nil
}

func ensureSocketDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return fmt.Errorf("%s must be a private root-owned directory (mode 0700)", path)
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket file %s", path)
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("a listener is already active on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("could not confirm %s is a stale socket: %w", path, err)
	}
	return os.Remove(path)
}
