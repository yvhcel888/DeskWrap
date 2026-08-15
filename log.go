// DeskWrap - ring-buffer logs shared by the service, the build process and
// the GUI (view + AI diagnosis). UTF-8-first decoding with system-ANSI
// fallback happens in proc.go before lines reach these buffers.
package main

import (
	"strings"
	"sync"
)

const maxLogLines = 400

type ringLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *ringLog) push(s string) {
	s = strings.ReplaceAll(s, "\r", "")
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > maxLogLines {
		r.lines = r.lines[len(r.lines)-maxLogLines:]
	}
}

func (r *ringLog) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) <= n {
		return append([]string{}, r.lines...)
	}
	return append([]string{}, r.lines[len(r.lines)-n:]...)
}

func (r *ringLog) reset() {
	r.mu.Lock()
	r.lines = nil
	r.mu.Unlock()
}

var (
	serviceLog ringLog
	buildLog   ringLog
)
