//go:build darwin
// +build darwin

package specific

import (
	"os/exec"
	"syscall"

	"github.com/rs/zerolog/log"
)

func Kill(existingServer *exec.Cmd) {

	// On UNIX-like systems, use syscall.Kill as before
	if err := syscall.Kill(-existingServer.Process.Pid, syscall.SIGKILL); err != nil {
		log.Error().Err(err).Msg("Failed to kill server process group")
	}
}
