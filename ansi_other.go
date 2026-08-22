//go:build !windows

package main

import "os"

func enableANSI(_ *os.File) bool { return true }
