package workspace

import (
	"io"
	"net"
	"sync"
)

type ExecutionStream interface {
	Conn() net.Conn
	Stdout() io.Reader
	Stderr() io.Reader
	WorkingDir() string
	ProcessID() *int
	Wait() error
	Close() error
}

type executionStream struct {
	conn       net.Conn
	stdout     io.Reader
	stderr     io.Reader
	workingDir string
	processID  *int
	waitFn     func() error
	closeFn    func() error
	killFn     func() error

	waitOnce  sync.Once
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

func newExecutionStream(conn net.Conn, stdout io.Reader, stderr io.Reader, workingDir string, processID *int, waitFn func() error, closeFn func() error, killFn func() error) *executionStream {
	return &executionStream{
		conn:       conn,
		stdout:     stdout,
		stderr:     stderr,
		workingDir: workingDir,
		processID:  processID,
		waitFn:     waitFn,
		closeFn:    closeFn,
		killFn:     killFn,
	}
}

func (stream *executionStream) Conn() net.Conn {
	return stream.conn
}

func (stream *executionStream) Stdout() io.Reader {
	return stream.stdout
}

func (stream *executionStream) Stderr() io.Reader {
	return stream.stderr
}

func (stream *executionStream) WorkingDir() string {
	return stream.workingDir
}

func (stream *executionStream) ProcessID() *int {
	return stream.processID
}

func (stream *executionStream) Wait() error {
	stream.waitOnce.Do(func() {
		if stream.waitFn == nil {
			return
		}
		stream.waitErr = stream.waitFn()
	})
	return stream.waitErr
}

func (stream *executionStream) Close() error {
	stream.closeOnce.Do(func() {
		if stream.closeFn == nil {
			return
		}
		stream.closeErr = stream.closeFn()
	})
	return stream.closeErr
}

func (stream *executionStream) Kill() error {
	if stream.killFn == nil {
		return nil
	}
	return stream.killFn()
}
