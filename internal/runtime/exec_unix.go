//go:build unix

package runtime

import (
	"os"
	"syscall"
)

// execProcess replaces serverpack with java so java becomes PID 1 and receives signals directly.
func execProcess(bin string, argv []string) error {
	return syscall.Exec(bin, argv, os.Environ())
}
