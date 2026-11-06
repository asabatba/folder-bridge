package fileops

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// installStagedFile keeps the previous destination intact until the entire
// staged, verified file is ready. Both paths are in the same directory/volume.
// On Windows, Go's os.Rename uses MoveFileExW(REPLACE_EXISTING), which fails
// without removing the old destination if another process denies delete access.
// On Unix, rename atomically replaces the old directory entry.
func installStagedFile(staged, dest string, originalHash *string) error {
	for attempt := 0; attempt < 5; attempt++ {
		if originalHash != nil {
			if err := CheckExpected(dest, *originalHash); err != nil {
				return err
			}
		}
		err := os.Rename(staged, dest)
		if err == nil {
			return nil
		}
		if !retryableReplaceError(err) || attempt == 4 {
			return fmt.Errorf("replace %q (previous contents preserved): %w", dest, err)
		}
		time.Sleep(time.Duration(80*(1<<attempt)) * time.Millisecond)
	}
	return errors.New("file replace failed")
}
