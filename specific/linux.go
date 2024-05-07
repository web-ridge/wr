//go:build linux
// +build linux

package specific

import (
	"os/exec"
	"strconv"

	"github.com/rs/zerolog/log"
)

func Kill(existingServer *exec.Cmd) {

	// On Windows, use taskkill to kill the process by PID
	cmd := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(existingServer.Process.Pid))
	if err := cmd.Run(); err != nil {
		log.Error().Err(err).Msg("Failed to kill server process")
	}
}
