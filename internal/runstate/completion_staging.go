package runstate

import (
	"io/fs"
	"os"
)

// completionTempHooks only replaces post-create operations in tests. Creation
// itself always goes through the rooted, no-replace platform implementation so
// the cleanup path is exercised against a real staging object.
type completionTempHooks struct {
	chmod   func(*os.File, fs.FileMode) error
	stat    func(*os.File) (fs.FileInfo, error)
	newFile func(uintptr, string) *os.File
}
