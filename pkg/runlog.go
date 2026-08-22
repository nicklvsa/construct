package pkg

import (
	"io"
	"sync"
)

const runLogCap = 16 * 1024

type runLogBuffer struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

func (b *runLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > runLogCap {
		b.buf = b.buf[len(b.buf)-runLogCap:]
		b.truncated = true
	}
	return len(p), nil
}

func (b *runLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated || len(b.buf) == 0 {
		return string(b.buf)
	}
	return "... [earlier output truncated]\n" + string(b.buf)
}

func (e *Executor) appendRunLog(name, s string) {
	if !e.recordLogs {
		return
	}
	e.mu.Lock()
	if e.logBufs == nil {
		e.logBufs = map[string]*runLogBuffer{}
	}
	b := e.logBufs[name]
	if b == nil {
		b = &runLogBuffer{}
		e.logBufs[name] = b
	}
	e.mu.Unlock()
	b.Write([]byte(s))
}

func (e *Executor) logRecorder(name string) io.Writer {
	if !e.recordLogs {
		return io.Discard
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.logBufs == nil {
		e.logBufs = map[string]*runLogBuffer{}
	}
	if b := e.logBufs[name]; b != nil {
		return b
	}
	b := &runLogBuffer{}
	e.logBufs[name] = b
	return b
}

func (e *Executor) takeRunLog(name string) string {
	if !e.recordLogs {
		return ""
	}
	e.mu.Lock()
	b := e.logBufs[name]
	delete(e.logBufs, name)
	e.mu.Unlock()
	if b == nil {
		return ""
	}
	return b.String()
}
