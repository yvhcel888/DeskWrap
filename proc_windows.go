//go:build windows

// DeskWrap - Windows process management.
//
// The core trick carried over from the original implementation: `.cmd`/`.bat`
// shims (pnpm/npm/npx/yarn) spawn child console apps that would open a
// visible console window. We parse the shim and launch `node <entry.js>`
// directly with CREATE_NO_WINDOW so no window ever appears - and paths with
// spaces stop breaking (no shell string joining).
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

const (
	createNoWindow     = 0x08000000
	createNewProcGroup = 0x00000200
)

// shimExpand replaces %~dp0 and %VAR% references with resolved values.
func shimExpand(s, shimDir string) string {
	re := regexp.MustCompile(`(?i)%~dp0`)
	s = re.ReplaceAllString(s, shimDir)
	s = os.Expand(s, func(k string) string { return os.Getenv(k) })
	return strings.TrimSpace(s)
}

// findCmdInPath locates a .cmd/.bat shim on PATH.
func findCmdInPath(program string) string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, dir := range dirs {
		for _, ext := range []string{".cmd", ".bat"} {
			full := filepath.Join(dir, program+ext)
			if fileExists(full) {
				return full
			}
		}
	}
	return ""
}

// unwrapShim parses a cmd shim and returns the real node.exe + entry script.
// Three patterns, in order of specificity (matching the proven JS logic):
//  1. npm 9+ variable pattern: SET "NODE_EXE=..." + SET "NPX_CLI_JS=..."
//  2. direct pattern (corepack, old npm): a whole line node "x.js" %*
//  3. call pattern: call "%~dp0\node.exe" "%~dp0\...\cli.js" %*
func unwrapShim(shimPath string, args []string) resolvedCommand {
	content, err := os.ReadFile(shimPath)
	if err != nil {
		return resolvedCommand{program: shimPath, args: args, viaShell: true}
	}
	shimDir := filepath.Dir(shimPath)
	text := string(content)

	// 1) Variable pattern (npm 9+). Matched first - most specific.
	if m := regexp.MustCompile(`(?i)SET\s+"NODE_EXE=([^"]+)"`).FindStringSubmatch(text); m != nil {
		if m2 := regexp.MustCompile(`(?i)SET\s+"(?:NPX|NPM)_CLI_JS=([^"]+)"`).FindStringSubmatch(text); m2 != nil {
			nodeBin := shimExpand(m[1], shimDir)
			entry := shimExpand(m2[1], shimDir)
			if nodeBin != "" && entry != "" && regexp.MustCompile(`(?i)\.c?js$`).MatchString(entry) {
				return resolvedCommand{
					program:  normalizeNodeName(nodeBin),
					args:     append([]string{entry}, args...),
					viaShell: false,
				}
			}
		}
	}

	// 2) Direct pattern. Anchored at line start so SET "_prog=node" and
	//    "%PATHEXT:;.JS" garbage don't false-positive.
	if m := regexp.MustCompile(`(?im)(?:^|\r\n)[ \t]*["']?([^"'\r\n]*?\bnode(?:\.exe)?)["']?[ \t]+["']([^"']+\.c?js)["']`).FindStringSubmatch(text); m != nil {
		return resolvedCommand{
			program:  normalizeNodeName(shimExpand(m[1], shimDir)),
			args:     append([]string{shimExpand(m[2], shimDir)}, args...),
			viaShell: false,
		}
	}

	// 3) call pattern.
	if m := regexp.MustCompile(`(?i)call\s+["']?([^"']+?node\.exe)["']?[ \t]+["']?([^"']+\.c?js)["']?`).FindStringSubmatch(text); m != nil {
		return resolvedCommand{
			program:  normalizeNodeName(shimExpand(m[1], shimDir)),
			args:     append([]string{shimExpand(m[2], shimDir)}, args...),
			viaShell: false,
		}
	}

	// Unknown shim: let cmd.exe run it with a hidden console.
	return resolvedCommand{program: shimPath, args: args, viaShell: true}
}

func normalizeNodeName(p string) string {
	if strings.EqualFold(p, "node") {
		return "node"
	}
	return p
}

type resolvedCommand struct {
	program  string
	args     []string
	viaShell bool
}

// resolveWindowsCommand maps a user-supplied program to a spawnable command.
//
// ENOENT fallback chain (stale PATH entries happen when the user moved or
// uninstalled Node): if the node.exe parsed from the shim no longer exists,
// retry with a working system node. If that fails too, fall back to running
// the shim through cmd.exe.
func resolveWindowsCommand(program string, args []string) resolvedCommand {
	lower := strings.ToLower(program)
	isShimName := strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")

	shim := ""
	if isShimName {
		if fileExists(program) {
			shim = program
		}
	} else if !strings.ContainsAny(program, `\/`) {
		// bare name (npm, pnpm...) - maybe a shim on PATH
		shim = findCmdInPath(program)
	}

	if shim == "" {
		return resolvedCommand{program: program, args: args, viaShell: false}
	}

	r := unwrapShim(shim, args)
	if r.viaShell {
		return r
	}
	if fileExists(r.program) {
		return r
	}

	// The parsed node.exe doesn't exist (stale PATH): retry with system node.
	if sysNode := findCmdInPath("node"); sysNode != "" {
		if s := unwrapShim(sysNode, nil); !s.viaShell && fileExists(s.program) && len(r.args) > 0 {
			return resolvedCommand{
				program:  s.program,
				args:     append([]string{r.args[0]}, args...),
				viaShell: false,
			}
		}
	}
	// Last resort: cmd.exe with a hidden console.
	return resolvedCommand{program: shim, args: args, viaShell: true}
}

// applyHiddenFlags marks the child process to run without a console window
// and in its own process group (so taskkill /T reaches the whole tree).
func applyHiddenFlags(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | createNewProcGroup,
		HideWindow:    true,
	}
}

// shellCommand runs an already-joined command line through cmd.exe.
func shellCommand(program string, args []string) *exec.Cmd {
	line := program
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	return exec.Command("cmd.exe", "/d", "/s", "/c", line)
}

// killProcessTree terminates the process and all its descendants.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	c := exec.Command("taskkill", "/pid", fmt.Sprint(pid), "/T", "/F")
	applyHiddenFlags(c)
	_ = c.Run()
}

// --- output decoding: strict UTF-8 first, then the system ANSI codepage ----

var ansiEnc = struct {
	done bool
	enc  encoding.Encoding
}{}

// systemANSIEncoding returns the encoding for the system ANSI codepage
// (936→GBK, 1252→windows-1252, 932→Shift_JIS, 950→Big5, 949→EUC-KR ...),
// read from the registry - never hard-coded to GBK, so logs stay correct
// on any locale.
func systemANSIEncoding() encoding.Encoding {
	if ansiEnc.done {
		return ansiEnc.enc
	}
	ansiEnc.done = true
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Nls\CodePage`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	// The CodePage values are REG_SZ strings ("936"), not DWORDs — reading
	// them with GetIntegerValue always fails with "unexpected key value
	// type" and silently disables the ANSI fallback (mojibake).
	acpStr, _, err := k.GetStringValue("ACP")
	if err != nil {
		return nil
	}
	acp, err := strconv.Atoi(strings.TrimSpace(acpStr))
	if err != nil {
		return nil
	}
	var enc encoding.Encoding
	switch acp {
	case 936:
		enc = simplifiedchinese.GBK
	case 54936:
		enc = simplifiedchinese.GB18030
	case 950:
		enc = traditionalchinese.Big5
	case 932:
		enc = japanese.ShiftJIS
	case 949:
		enc = korean.EUCKR
	case 1250:
		enc = charmap.Windows1250
	case 1251:
		enc = charmap.Windows1251
	case 1252:
		enc = charmap.Windows1252
	case 1253:
		enc = charmap.Windows1253
	case 1254:
		enc = charmap.Windows1254
	case 1255:
		enc = charmap.Windows1255
	case 1256:
		enc = charmap.Windows1256
	case 1257:
		enc = charmap.Windows1257
	case 1258:
		enc = charmap.Windows1258
	case 437:
		enc = charmap.CodePage437
	case 850:
		enc = charmap.CodePage850
	case 852:
		enc = charmap.CodePage852
	case 866:
		enc = charmap.CodePage866
	default:
		return nil
	}
	ansiEnc.enc = enc
	return enc
}

// decodeChunk decodes a byte chunk as UTF-8; if invalid, it holds back a
// possible partial trailing character and falls back to the ANSI codepage.
type decodeChunk struct {
	pending []byte
}

func (d *decodeChunk) decode(b []byte) string {
	b = append(d.pending, b...)
	d.pending = nil

	if utf8.Valid(b) {
		return string(b)
	}
	// Maybe a multibyte character got split across reads: hold back the tail.
	for n := 1; n <= 3 && n < len(b); n++ {
		if utf8.Valid(b[:len(b)-n]) {
			d.pending = append([]byte{}, b[len(b)-n:]...)
			return string(b[:len(b)-n])
		}
	}
	// Genuinely not UTF-8 (e.g. GBK console output): use the ANSI codepage.
	if enc := systemANSIEncoding(); enc != nil {
		if out, _, err := transform.Bytes(enc.NewDecoder(), b); err == nil {
			return string(out)
		}
	}
	return string(b)
}

// streamToLog pipes child output into a ring log (decoded) and echoes it to
// our stdout, mirroring the original "service log" behaviour.
func streamToLog(r io.Reader, dst *ringLog, prefix string) {
	buf := make([]byte, 8192)
	var dec decodeChunk
	for {
		n, err := r.Read(buf)
		if n > 0 {
			text := dec.decode(buf[:n])
			if prefix != "" && text != "" {
				_, _ = io.WriteString(os.Stdout, prefix+text)
				if !strings.HasSuffix(text, "\n") {
					_, _ = io.WriteString(os.Stdout, "\n")
				}
			}
			dst.push(text)
		}
		if err != nil {
			return
		}
	}
}

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")

	procGetUserDefaultUILanguage = kernel32.NewProc("GetUserDefaultUILanguage")
)

// systemLocale returns the system UI language (e.g. "zh-CN", "en-US").
func systemLocale() string {
	langID, _, _ := procGetUserDefaultUILanguage.Call()
	primary := uint16(langID) & 0x3FF
	switch primary {
	case 0x04: // zh
		return "zh-CN"
	case 0x11: // ja
		return "ja-JP"
	case 0x12: // ko
		return "ko-KR"
	default:
		return "en-US"
	}
}
