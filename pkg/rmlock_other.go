//go:build !windows

package pkg

// isDeletionLocked is a Windows-only concept: elsewhere the OS does not lock
// files against deletion, so no error can mean that.
func isDeletionLocked(err error) bool {
	return false
}

// terminateProcessesUsing is a Windows-only concept: elsewhere a running
// executable can be deleted (or replaced) directly, so there is nothing to
// terminate.
func terminateProcessesUsing(path string) error {
	return nil
}
