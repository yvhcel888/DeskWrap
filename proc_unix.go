//go:build !windows

// DeskWrap - Unix process management (best effort; Windows is the primary
// target but the codebase stays buildable everywhere).
package main

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/text/transform"
)

type resolvedCommand struct {
	program  string
	args     []string
	viaShell bool
}

func resolveWindowsCommand(program string, args []string) resolvedCommand {
	return resolvedCommand{program: program, args: args, viaShell: false}
}

func applyHiddenFlags(c *exec.Cmd) {
	// Detach from our process group so we can signal the whole group.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func shellCommand(program string, args []string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", program+" "+strings.Join(args, " "))
}

func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) // process group
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// ANSI-codepage decoding is a Windows concept; on Unix, UTF-8 only.
func systemANSIEncoding() transform.Transformer {
	return nil
}

type decodeChunk struct{}

func (d *decodeChunk) decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return string(b) // bytes.ToValidUTF8 is overkill for logs
}

func streamToLog(r io.Reader, dst *ringLog, prefix string) {
	buf := make([]byte, 8192)
	var dec decodeChunk
	for {
		n, err := r.Read(buf)
		if n > 0 {
			text := dec.decode(buf[:n])
			if prefix != "" && text != "" {
				_, _ = io.WriteString(os.Stdout, prefix+text)
			}
			dst.push(text)
		}
		if err != nil {
			return
		}
	}
}

func systemLocale() string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			s = strings.SplitN(s, ".", 2)[0]
			return strings.ReplaceAll(s, "_", "-")
		}
	}
	return "en-US"
}
