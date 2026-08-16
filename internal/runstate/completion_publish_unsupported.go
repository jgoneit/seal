//go:build !darwin && !linux && !windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
)

func publishCompletionNoReplace(*os.Root, string, string, fs.FileInfo) error {
	return errors.New("atomic no-replace completion publication is unsupported on this platform")
}
