//go:build !windows

package main

func retryableReplaceError(error) bool { return false }
