// Package tmux wraps the tmux CLI for session management.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
)

// SessionFromPath attaches to (or creates then attaches to) a tmux session
// rooted at filePath, optionally running cmd in the new session.
func SessionFromPath(name, filePath string, cmd ...string) error {
	if filePath == "" {
		return fmt.Errorf("file path is required")
	}

	exec.Command("tmux", "start-server").Run()
	if err := exec.Command("tmux", "has-session", "-t", name).Run(); err == nil {
		return AttachOrSwitch(name)
	}

	out, err := exec.Command("tmux", "new-session", "-c", filePath, "-d", "-s", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("new-session: %w\n%s", err, out)
	}

	exec.Command("tmux", "send-keys", "-t", name, fmt.Sprintf("echo -ne '\\033]0;%s\\007'", name), "C-m").Run()
	if len(cmd) > 0 && cmd[0] != "" {
		exec.Command("tmux", "send-keys", "-t", name, cmd[0], "C-m").Run()
	}
	return AttachOrSwitch(name)
}

// AttachOrSwitch switches the client if already inside tmux, else attaches.
func AttachOrSwitch(name string) error {
	if os.Getenv("TMUX") != "" {
		return exec.Command("tmux", "switch-client", "-t", name).Run()
	}
	return exec.Command("tmux", "attach-session", "-t", name).Run()
}

// Popup opens an 80%x80% tmux display-popup running cmd in path.
func Popup(path string, cmd string) error {
	return exec.Command("tmux", "display-popup", "-d", path, "-w", "80%", "-h", "80%", "-E", "/bin/bash", "-lc", cmd).Run()
}
