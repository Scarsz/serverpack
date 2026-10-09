//go:build !unix

package runtime

import (
	"errors"
	"os"
	"os/exec"
)

// execProcess runs the server as a child process on platforms without exec(2) and exits with its status.
func execProcess(bin string, argv []string) error {
	cmd := exec.Command(bin, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	return err
}
