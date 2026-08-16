//go:build windows

package runstate

import (
	"testing"

	"golang.org/x/sys/windows"
)

const windowsFileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED |
	windows.SYNCHRONIZE |
	0x1ff

func windowsFileMaskHasFullControl(mask windows.ACCESS_MASK) bool {
	return mask&windows.GENERIC_ALL != 0 || mask&windowsFileAllAccess == windowsFileAllAccess
}

func TestWindowsFileMaskHasFullControlNormalizesGenericRights(t *testing.T) {
	tests := []struct {
		name string
		mask windows.ACCESS_MASK
		want bool
	}{
		{name: "generic all", mask: windows.GENERIC_ALL, want: true},
		{name: "expanded file all access", mask: windowsFileAllAccess, want: true},
		{name: "expanded with extra rights", mask: windowsFileAllAccess | windows.ACCESS_SYSTEM_SECURITY, want: true},
		{name: "generic file rights", mask: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE},
		{name: "no rights"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsFileMaskHasFullControl(test.mask); got != test.want {
				t.Fatalf("windowsFileMaskHasFullControl(%#x) = %v, want %v", test.mask, got, test.want)
			}
		})
	}
}
