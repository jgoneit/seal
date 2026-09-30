//go:build windows

package runstate

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// privateSecurityDescriptor builds a protected DACL that grants GENERIC_ALL
// only to the current process-token user and LocalSystem, with the given ACE
// inheritance. pinner keeps the SIDs, ACL, and descriptor fixed for the
// native create call; the caller unpins it afterwards.
func privateSecurityDescriptor(pinner *runtime.Pinner, inheritance uint32) (*windows.SECURITY_DESCRIPTOR, error) {
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	currentUserSID, err := currentUser.User.Sid.Copy()
	if err != nil {
		return nil, err
	}
	localSystemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	pinner.Pin(currentUserSID)
	pinner.Pin(localSystemSID)

	allowedSIDs := []*windows.SID{currentUserSID}
	if !currentUserSID.Equals(localSystemSID) {
		allowedSIDs = append(allowedSIDs, localSystemSID)
	}
	entries := make([]windows.EXPLICIT_ACCESS, len(allowedSIDs))
	for index, sid := range allowedSIDs {
		entries[index] = windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	runtime.KeepAlive(entries)
	if err != nil {
		return nil, err
	}
	pinner.Pin(acl)
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	pinner.Pin(descriptor)
	return descriptor, nil
}
