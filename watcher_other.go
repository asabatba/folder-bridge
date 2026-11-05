//go:build !windows

package main

import (
	"context"
	"errors"
)

func watchFolder(_ context.Context, _ string, _ func(string)) error {
	return errors.New("native filesystem notifications are currently Windows-only")
}
