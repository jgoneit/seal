//go:build windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCreatePrivateCompletionTempAppliesProtectedDACL(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	const name = ".completion-0123456789abcdef0123456789abcdef.tmp"
	file, info, err := createPrivateCompletionTemp(root, name)
	if err != nil {
		t.Fatalf("createPrivateCompletionTemp(): %v", err)
	}
	if info == nil || !info.Mode().IsRegular() {
		_ = file.Close()
		t.Fatalf("created info = %v, want regular file", info)
	}
	assertPrivateCompletionDACL(t, windows.Handle(file.Fd()))
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	defer root.Remove(name)

	if _, _, err := createPrivateCompletionTemp(root, name); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("collision error = %v, want fs.ErrExist", err)
	}
}

func assertPrivateCompletionDACL(t *testing.T, handle windows.Handle) {
	t.Helper()
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo(): %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("Control(): %v", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("completion DACL control = %#x, want SE_DACL_PROTECTED", control)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("DACL(): %v", err)
	}
	if dacl == nil || defaulted {
		t.Fatalf("DACL = %v, defaulted = %v", dacl, defaulted)
	}
	allowed := privateStagingSIDSet(t)
	if int(dacl.AceCount) != len(allowed) {
		t.Fatalf("ACE count = %d, want %d", dacl.AceCount, len(allowed))
	}
	seen := make(map[string]struct{}, len(allowed))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatalf("GetAce(%d): %v", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&windows.GENERIC_ALL == 0 {
			t.Fatalf("ACE %d type/mask = %d/%#x", index, ace.Header.AceType, ace.Mask)
		}
		flags := uint32(ace.Header.AceFlags)
		if flags&(windows.INHERITED_ACE|windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != 0 {
			t.Fatalf("ACE %d flags = %#x, want explicit file-only ACE", index, flags)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		key := sid.String()
		if _, ok := allowed[key]; !ok {
			t.Fatalf("ACE %d SID = %q, want current user or LocalSystem", index, key)
		}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate ACE SID %q", key)
		}
		seen[key] = struct{}{}
	}
}
