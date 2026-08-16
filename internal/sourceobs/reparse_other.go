//go:build !windows

package sourceobs

import "io/fs"

func isReparsePoint(fs.FileInfo) bool { return false }
