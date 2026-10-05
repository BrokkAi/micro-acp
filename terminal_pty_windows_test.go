//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ptyProcess runs a command attached to a ConPTY pseudo console. Reads return
// the VT output the console host renders; writes are typed into it.
type ptyProcess struct {
	console   windows.Handle
	in, out   *os.File
	process   *os.Process
	stop      func() bool
	closeOnce sync.Once
}

func startPTY(ctx context.Context, argv []string, cols, rows int) (_ *ptyProcess, err error) {
	var consoleIn, in, out, consoleOut windows.Handle
	if err := windows.CreatePipe(&consoleIn, &in, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&out, &consoleOut, nil, 0); err != nil {
		windows.CloseHandle(consoleIn)
		windows.CloseHandle(in)
		return nil, err
	}
	var console windows.Handle
	err = windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, consoleIn, consoleOut, 0, &console)
	// The console host keeps its own copies of these pipe ends.
	windows.CloseHandle(consoleIn)
	windows.CloseHandle(consoleOut)
	if err != nil {
		windows.CloseHandle(in)
		windows.CloseHandle(out)
		return nil, err
	}
	p := &ptyProcess{console: console, in: os.NewFile(uintptr(in), "conpty-in"), out: os.NewFile(uintptr(out), "conpty-out")}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	// The attribute value is the console handle itself, not its address.
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&console)), unsafe.Sizeof(console)); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	// Without explicit handles the child can pick up this process's redirected
	// stdio (go test's pipes) instead of the pseudo console.
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = windows.InvalidHandle, windows.InvalidHandle, windows.InvalidHandle
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, err
	}
	var info windows.ProcessInformation
	if err = windows.CreateProcess(nil, commandLine, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &startup.StartupInfo, &info); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(info.Process)
	windows.CloseHandle(info.Thread)
	// os.Process holds its own handle, so Wait and Kill can run concurrently.
	if p.process, err = os.FindProcess(int(info.ProcessId)); err != nil {
		windows.TerminateProcess(info.Process, 1)
		return nil, err
	}
	p.stop = context.AfterFunc(ctx, p.Kill)
	return p, nil
}

func (p *ptyProcess) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *ptyProcess) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *ptyProcess) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.console, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (p *ptyProcess) Wait() error {
	state, err := p.process.Wait()
	if err != nil {
		return err
	}
	if !state.Success() {
		return errors.New(state.String())
	}
	return nil
}

func (p *ptyProcess) Kill() {
	if p.process != nil {
		_ = p.process.Kill()
	}
}

// Close ends the console host first. Older Windows versions block in
// ClosePseudoConsole until its final frame is read, so the output pipe stays
// open for the reader until then.
func (p *ptyProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.stop != nil {
			p.stop()
		}
		windows.ClosePseudoConsole(p.console)
		_ = p.in.Close()
		_ = p.out.Close()
	})
	return nil
}
