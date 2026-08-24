//go:build windows

package database

import "syscall"

// syscallECONNREFUSED enables transport-error detection on Windows builds
// (development hosts and integration runners).
var syscallECONNREFUSED = syscall.ECONNREFUSED
