//go:build linux || darwin

package main

import (
	"context"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// ptyProcess runs a command attached to a pseudo-terminal. Reads return what
// the program draws; writes are typed into it.
type ptyProcess struct {
	*os.File
	cmd *exec.Cmd
}

func startPTY(ctx context.Context, argv []string, cols, rows int) (*ptyProcess, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, err
	}
	return &ptyProcess{File: terminal, cmd: cmd}, nil
}

func (p *ptyProcess) Resize(cols, rows int) error {
	return pty.Setsize(p.File, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (p *ptyProcess) Wait() error { return p.cmd.Wait() }

func (p *ptyProcess) Kill() { _ = p.cmd.Process.Kill() }
