//go:build ignore

// ttycheck exchanges binary data through the kernel serial devices in the QEMU lab.
package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func open(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	term, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err == nil {
		term.Iflag, term.Oflag, term.Lflag = 0, 0, 0
		term.Cflag = unix.B115200 | unix.CS8 | unix.CLOCAL | unix.CREAD
		term.Cc[unix.VMIN], term.Cc[unix.VTIME] = 1, 0
		err = unix.IoctlSetTermios(fd, unix.TCSETS, term)
	}
	if err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func transfer(fd int, data []byte, write bool, deadline time.Time) (int, error) {
	events, operation := int16(unix.POLLIN), unix.Read
	if write {
		events, operation = unix.POLLOUT, unix.Write
	}
	completed := 0
	for completed < len(data) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return completed, os.ErrDeadlineExceeded
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: events}}
		_, err := unix.Poll(poll, int(remaining.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return completed, err
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return completed, io.ErrClosedPipe
		}
		if poll[0].Revents&events == 0 {
			continue
		}
		n, err := operation(fd, data[completed:])
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return completed, err
		}
		if n == 0 {
			return completed, io.EOF
		}
		completed += n
	}
	return completed, nil
}

func echo(path string) {
	for {
		fd, err := open(path)
		if err == nil {
			serveEcho(fd)
			unix.Close(fd)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func serveEcho(fd int) {
	var data [4096]byte
	for {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 500)
		if err == unix.EINTR {
			continue
		}
		if err != nil || poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, data[:])
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			return
		}
		if _, err := transfer(fd, data[:n], true, time.Now().Add(60*time.Second)); err != nil {
			return
		}
	}
}

func check(path string, size int, seed int64, timeout time.Duration) error {
	fd, err := open(path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	started := time.Now()
	deadline := started.Add(timeout)
	want := make([]byte, size)
	rand.New(rand.NewSource(seed)).Read(want)
	sent := make(chan error, 1)
	go func() {
		n, err := transfer(fd, want, true, deadline)
		if err != nil {
			err = fmt.Errorf("sent %d of %d bytes: %w", n, size, err)
		}
		sent <- err
	}()
	got := make([]byte, size)
	n, readErr := transfer(fd, got[:min(size, 4096)], false, deadline)
	if readErr == nil {
		fmt.Printf("received initial %d bytes\n", n)
		var rest int
		rest, readErr = transfer(fd, got[n:], false, deadline)
		n += rest
	}
	if err := <-sent; err != nil {
		return err
	}
	if readErr != nil {
		return fmt.Errorf("received %d of %d bytes: %w", n, size, readErr)
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("echo mismatch: got sha256 %x, want %x", sha256.Sum256(got), sha256.Sum256(want))
	}
	fmt.Printf("verified %d bytes each way in %d ms, sha256=%x\n", size, time.Since(started).Milliseconds(), sha256.Sum256(got))
	return nil
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "echo" {
		echo(os.Args[2])
	}
	if len(os.Args) != 6 || os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: ttycheck echo PATH | ttycheck check PATH BYTES SEED TIMEOUT")
		os.Exit(2)
	}
	size, sizeErr := strconv.Atoi(os.Args[3])
	seed, seedErr := strconv.ParseInt(os.Args[4], 10, 64)
	timeout, timeoutErr := time.ParseDuration(os.Args[5])
	if sizeErr != nil || seedErr != nil || timeoutErr != nil || size < 1 || size > 1<<20 || timeout <= 0 {
		fmt.Fprintln(os.Stderr, "invalid size, seed, or timeout")
		os.Exit(2)
	}
	if err := check(os.Args[2], size, seed, timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
