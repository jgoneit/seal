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

func TestCreatePrivateStagingDirectoryAppliesProtectedInheritedDACL(t *testing.T) {
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	const name = ".tmp-0123456789abcdef0123456789abcdef"
	if err := createPrivateStagingDirectory(parent, name); err != nil {
		t.Fatalf("createPrivateStagingDirectory(): %v", err)
	}
	defer parent.RemoveAll(name)

	directory, err := parent.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateStagingDACL(t, windows.Handle(directory.Fd()), false)
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}

	staging, err := parent.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := staging.OpenFile("artifact", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = staging.Close()
		t.Fatal(err)
	}
	assertPrivateStagingDACL(t, windows.Handle(artifact.Fd()), true)
	if err := artifact.Close(); err != nil {
		_ = staging.Close()
		t.Fatal(err)
	}
	if err := staging.Close(); err != nil {
		t.Fatal(err)
	}

	if err := createPrivateStagingDirectory(parent, name); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("collision error = %v, want fs.ErrExist", err)
	}
}

func assertPrivateStagingDACL(t *testing.T, handle windows.Handle, inherited bool) {
	t.Helper()

	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetSecurityInfo(): %v", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("Control(): %v", err)
	}
	if !inherited && control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("directory DACL control = %#x, want SE_DACL_PROTECTED", control)
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
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d type = %d, want ACCESS_ALLOWED_ACE_TYPE", index, ace.Header.AceType)
		}
		if ace.Mask&windows.GENERIC_ALL == 0 {
			t.Fatalf("ACE %d mask = %#x, want GENERIC_ALL", index, ace.Mask)
		}
		flags := uint32(ace.Header.AceFlags)
		if inherited {
			if flags&windows.INHERITED_ACE == 0 {
				t.Fatalf("artifact ACE %d flags = %#x, want INHERITED_ACE", index, flags)
			}
		} else {
			want := uint32(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
			if flags&want != want {
				t.Fatalf("directory ACE %d flags = %#x, want object and container inheritance", index, flags)
			}
			if flags&windows.INHERITED_ACE != 0 {
				t.Fatalf("directory ACE %d flags = %#x, want explicit ACE", index, flags)
			}
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

func privateStagingSIDSet(t *testing.T) map[string]struct{} {
	t.Helper()
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	localSystem, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]struct{}{
		currentUser.User.Sid.String(): {},
		localSystem.String():          {},
	}
}
