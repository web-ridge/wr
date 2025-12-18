//go:build linux
// +build linux

package specific

import (
	"os/exec"
	"syscall"

	"github.com/rs/zerolog/log"
)

func Kill(existingServer *exec.Cmd) {
	// On Linux, use syscall.Kill to kill the process group (negative PID)
	if err := syscall.Kill(-existingServer.Process.Pid, syscall.SIGKILL); err != nil {
		log.Error().Err(err).Msg("Failed to kill server process group")
	}
}
