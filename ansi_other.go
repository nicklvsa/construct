//go:build !windows

package main

import "os"

func enableANSI(f *os.File) bool { return true }
