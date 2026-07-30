//go:build windows

package plan_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestPlanSaveProtectsWindowsDACLForCurrentUser(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secure.r42plan")
	_, err := plan.Save(path, validPlan(t))
	require.NoError(t, err)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	sddl := descriptor.String()

	assert.Contains(t, sddl, user.User.Sid.String())
	assert.Equal(t, 1, strings.Count(sddl, "(A;"), sddl)
}
