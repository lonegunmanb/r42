//go:build windows

package plan

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func restrictPlanFile(path string, _ *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:P(A;;FA;;;" + user.User.Sid.String() + ")",
	)
	if err != nil {
		return fmt.Errorf("build current-user security descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read current-user dacl: %w", err)
	}
	if err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("set current-user dacl: %w", err)
	}
	return nil
}
