package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (provider *LocalProvider) ExecuteStream(ctx context.Context, command string, args []string, dir string) (ExecutionStream, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("workspace command is required")
	}
	workingDir := strings.TrimSpace(dir)
	if workingDir == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	absDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, err
	}
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = absDir
	stdin, err := process.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := process.Start(); err != nil {
		return nil, err
	}
	clientConn, serverConn := net.Pipe()
	go func() {
		_, _ = io.Copy(stdin, serverConn)
		_ = stdin.Close()
		_ = serverConn.Close()
	}()
	var processID *int
	if process.Process != nil {
		pid := process.Process.Pid
		processID = &pid
	}
	closeProcess := func() error {
		var closeErr error
		if err := clientConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = err
		}
		if err := serverConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && closeErr == nil {
			closeErr = err
		}
		if process.Process != nil {
			_ = process.Process.Signal(syscall.SIGTERM)
		}
		return closeErr
	}
	killProcess := func() error {
		if process.Process == nil {
			return nil
		}
		err := process.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	return newExecutionStream(clientConn, stdout, stderr, absDir, processID, process.Wait, closeProcess, killProcess), nil
}
