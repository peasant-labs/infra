//go:build unix

package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// openConfined opens each component relative to the repository root without
// following any symlink, and retains the file descriptor through staging.
func openConfined(name string) (*os.File, error) {
	dir, err := unix.Open(".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		next, e := unix.Openat(dir, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(dir)
		if e != nil {
			return nil, e
		}
		dir = next
	}
	fd, err := unix.Openat(dir, parts[len(parts)-1], unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	_ = unix.Close(dir)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("cannot write a file with multiple hard links")
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return f, nil
}
