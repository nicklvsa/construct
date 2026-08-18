//go:build !windows

package pkg

func isDeletionLocked(_ error) bool {
	return false
}

func terminateProcessesUsing(_ string) error {
	return nil
}
