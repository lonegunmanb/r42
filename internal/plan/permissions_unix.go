//go:build !windows

package plan

import "os"

func restrictPlanFile(_ string, file *os.File) error {
	return file.Chmod(0o600)
}
