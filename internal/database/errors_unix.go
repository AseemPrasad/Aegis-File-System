//go:build !windows

package database

import "syscall"

// syscallECONNREFUSED enables transport-error detection on Linux/production.
var syscallECONNREFUSED = syscall.ECONNREFUSED
