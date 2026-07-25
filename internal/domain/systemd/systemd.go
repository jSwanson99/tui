package systemd

import (
	"os/exec"
)

type Service struct {
	Name string
}

func (s *Service) IsActive() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", s.Name).Run() == nil
}
func (s *Service) Start() bool {
	return exec.Command("systemctl", "--user", "start", s.Name).Run() == nil
}
