package sip

import "os/exec"

// ExecRunner lanza procesos reales con os/exec. Implementa ProcRunner.
type ExecRunner struct{}

func NewExecRunner() *ExecRunner { return &ExecRunner{} }

type execProc struct {
	cmd *exec.Cmd
}

func (r *ExecRunner) Start(name string, args []string) (Proc, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd}, nil
}

func (p *execProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *execProc) Wait() error { return p.cmd.Wait() }
