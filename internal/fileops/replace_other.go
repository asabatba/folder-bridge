//go:build !windows

package fileops

func retryableReplaceError(error) bool { return false }
