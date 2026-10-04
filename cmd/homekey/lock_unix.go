//go:build linux || darwin || freebsd || openbsd || netbsd

// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
	"syscall"
)

func lockState(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory is in use: stop the other process first")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
