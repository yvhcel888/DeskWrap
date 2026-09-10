//go:build windows

// DeskWrap unit tests - pure logic pieces (shim parsing, JSON extraction,
// config merging, detection). GUI/WebView2 boot is covered separately.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- cmd shim unwrapping (the 3 proven patterns) ----------------------------

func writeShim(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnwrapShimVariablePattern(t *testing.T) {
	dir := t.TempDir()
	shim := writeShim(t, dir, "npx.cmd",
		"@ECHO off\r\nSET \"NODE_EXE=%~dp0\\node.exe\"\r\nSET \"NPX_CLI_JS=%~dp0\\node_modules\\npm\\bin\\npx-cli.js\"\r\n\"%NODE_EXE%\" \"%NPX_CLI_JS%\" %*\r\n")
	r := unwrapShim(shim, []string{"--version"})
	if r.viaShell {
		t.Fatal("variable pattern should unwrap")
	}
	if filepath.ToSlash(r.program) != filepath.ToSlash(filepath.Join(dir, "node.exe")) {
		t.Fatalf("program = %q", r.program)
	}
	wantEntry := filepath.Join(dir, "node_modules", "npm", "bin", "npx-cli.js")
	if filepath.ToSlash(r.args[0]) != filepath.ToSlash(wantEntry) || r.args[1] != "--version" {
		t.Fatalf("args = %v", r.args)
	}
}

func TestUnwrapShimDirectPattern(t *testing.T) {
	dir := t.TempDir()
	shim := writeShim(t, dir, "pnpm.cmd",
		"@ECHO off\r\nnode \"%~dp0\\..\\pnpm\\bin\\pnpm.cjs\" %*\r\n")
	r := unwrapShim(shim, []string{"install"})
	if r.viaShell {
		t.Fatal("direct pattern should unwrap")
	}
	if r.program != "node" {
		t.Fatalf("program = %q", r.program)
	}
	if filepath.Base(r.args[0]) != "pnpm.cjs" || r.args[1] != "install" {
		t.Fatalf("args = %v", r.args)
	}
}

func TestUnwrapShimCallPattern(t *testing.T) {
	dir := t.TempDir()
	shim := writeShim(t, dir, "npm.cmd",
		"@ECHO off\r\ncall \"%~dp0\\node.exe\" \"%~dp0\\node_modules\\npm\\bin\\npm-cli.js\" %*\r\n")
	r := unwrapShim(shim, nil)
	if r.viaShell {
		t.Fatal("call pattern should unwrap")
	}
	if filepath.ToSlash(r.program) != filepath.ToSlash(filepath.Join(dir, "node.exe")) {
		t.Fatalf("program = %q", r.program)
	}
	if filepath.Base(r.args[0]) != "npm-cli.js" {
		t.Fatalf("args = %v", r.args)
	}
}

func TestUnwrapShimUnknownFallsBackToShell(t *testing.T) {
	dir := t.TempDir()
	shim := writeShim(t, dir, "weird.cmd", "@ECHO off\r\nsetlocal\r\nweird-native-tool %*\r\n")
	r := unwrapShim(shim, nil)
	if !r.viaShell {
		t.Fatal("unknown shim must fall back to shell")
	}
}

func TestUnwrapShimNoFalsePositiveOnSetProg(t *testing.T) {
	// The direct pattern must NOT match SET "_prog=node" lines (historical bug).
	dir := t.TempDir()
	shim := writeShim(t, dir, "npm.cmd",
		"@ECHO off\r\nSET \"_prog=node\"\r\nSET \"PATHEXT=%PATHEXT:;.JS;=%\"\r\ncall \"%~dp0\\node.exe\" \"%~dp0\\node_modules\\npm\\bin\\npm-cli.js\" %*\r\n")
	r := unwrapShim(shim, nil)
	if r.viaShell {
		t.Fatal("should still unwrap via call pattern")
	}
	if filepath.Base(r.args[0]) != "npm-cli.js" {
		t.Fatalf("args = %v", r.args)
	}
}

// --- config merging ----------------------------------------------------------

func TestDeepMerge(t *testing.T) {
	base := map[string]any{
		"a":      float64(1),
		"nested": map[string]any{"x": "keep", "y": float64(2)},
		"arr":    []any{"a"},
	}
	extra := map[string]any{
		"b":      float64(3),
		"nested": map[string]any{"y": float64(9)},
		"arr":    []any{"b"},
	}
	out := deepMerge(base, extra)
	if out["a"] != float64(1) || out["b"] != float64(3) {
		t.Fatal("scalar merge wrong")
	}
	n := out["nested"].(map[string]any)
	if n["x"] != "keep" || n["y"] != float64(9) {
		t.Fatal("nested merge wrong")
	}
	if out["arr"].([]any)[0] != "b" {
		t.Fatal("arrays must be replaced, not merged")
	}
}

// --- project detection -------------------------------------------------------

func TestDetectNodeProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"x","scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := detect(dir)
	if d["type"] != "node" {
		t.Fatalf("type = %v", d["type"])
	}
	if d["command"] != "npm" {
		t.Fatalf("command = %v", d["command"])
	}
	if d["port"] != 5173 {
		t.Fatalf("vite port = %v, want 5173", d["port"])
	}
}

func TestDetectPythonProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"),
		[]byte("import gradio\ngr.Interface().launch()"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := detect(dir)
	if d["type"] != "python" || d["port"] != 7860 {
		t.Fatalf("detect = %v, want python/7860", d)
	}
}

func TestDetectEmptyDir(t *testing.T) {
	d := detect(t.TempDir())
	if d["type"] != "unknown" {
		t.Fatalf("type = %v, want unknown", d["type"])
	}
}

// --- decode chunk (UTF-8 partial hold-back) ----------------------------------

func TestDecodeChunkSplitMultibyte(t *testing.T) {
	var d decodeChunk
	full := []byte("中文日志 line")
	if got := d.decode(full); got != string(full) {
		t.Fatalf("full decode = %q", got)
	}
	var d2 decodeChunk
	first := []byte("中文")[:5] // cut 文 in half
	second := []byte("中文")[5:]
	out1 := d2.decode(first)
	out2 := d2.decode(second)
	if out1+out2 != "中文" {
		t.Fatalf("split decode = %q + %q", out1, out2)
	}
}

// --- artifact name sanitization ----------------------------------------------

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName(`bad<>:"/\|?*name`); got != "badname" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := sanitizeName("  "); got != "DeskWrap" {
		t.Fatalf("empty sanitize = %q", got)
	}
	if got := sanitizeName("My App 1.0"); strings.ContainsAny(got, `<>:"/\|?*`) {
		t.Fatalf("sanitize left invalid chars: %q", got)
	}
}

// --- ring log concurrency ----------------------------------------------------

// push is called from the stdout and stderr pump goroutines at the same time;
// it must hold the mutex so concurrent push/tail/reset cannot corrupt lines.
func TestRingLogConcurrentPush(t *testing.T) {
	var r ringLog
	var wg sync.WaitGroup
	for _, chunk := range []string{"alpha\nbeta\n", "gamma\n"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.push(c)
			}
		}(chunk)
	}
	for i := 0; i < 50; i++ {
		_ = r.tail(10)
	}
	wg.Wait()
	lines := r.tail(maxLogLines * 2)
	if len(lines) > maxLogLines {
		t.Fatalf("ring log grew beyond cap: %d", len(lines))
	}
	for _, l := range lines {
		if l != "alpha" && l != "beta" && l != "gamma" {
			t.Fatalf("corrupted line: %q", l)
		}
	}
}

// --- streamlit detection uses the real entry file -----------------------------

func TestDetectStreamlitAppPy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "streamlit_app.py"),
		[]byte("import streamlit as st\nst.write('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := detect(dir)
	if d["type"] != "python" || d["command"] != "streamlit" {
		t.Fatalf("detect = %v, want python/streamlit", d)
	}
	args := stringSlice(d["args"])
	if len(args) != 2 || args[0] != "run" || args[1] != "streamlit_app.py" {
		t.Fatalf("args = %v, want [run streamlit_app.py]", args)
	}
	if d["port"] != 8501 {
		t.Fatalf("port = %v, want 8501", d["port"])
	}
}

// --- deps-install log chunks --------------------------------------------------

// runInstallDeps reads the install command's output in 512-byte chunks and
// splices each one into a JS string literal. Two independent requirements:
// the fragment must be escaped as a JS *and* JSON string literal (npm/pnpm
// emit \r\n, quotes and backslashes), and a multi-byte character split
// across a chunk boundary must not be mangled into U+FFFD.
func TestDepsLogChunkEscaping(t *testing.T) {
	// pnpm 进度条 style: carriage returns, backslashes, single quotes.
	raw := "added 12 packages\r\nC:\\Users\\tzj\\.pnpm\\store\r\ndone 'install' `x`"
	enc := jsonString(raw)
	if !strings.HasPrefix(enc, `"`) || !strings.HasSuffix(enc, `"`) {
		t.Fatalf("not a JS string literal: %s", enc)
	}
	if strings.ContainsAny(enc[1:len(enc)-1], "\r\n") {
		t.Fatalf("raw CR/LF survived into the Eval source: %q", enc)
	}
	if !strings.Contains(enc, `\r\n`) {
		t.Fatalf("CR/LF must be escaped as \\r\\n, got %q", enc)
	}
	if !strings.Contains(enc, `\\Users`) {
		t.Fatalf("backslashes must stay escaped, got %q", enc)
	}
	// encode → decode must round-trip: the depsBox must show what npm printed.
	var back string
	if err := json.Unmarshal([]byte(enc), &back); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if back != raw {
		t.Fatalf("round-trip = %q, want %q", back, raw)
	}
}

// A multi-byte character split across a read boundary must survive. The
// realistic shape is "ASCII text, then a 3-byte character cut in half" —
// npm/pnpm output line by line, streamed 512 bytes at a time. Reassembling
// the per-chunk jsonString fragments must spell out the whole text; a raw
// jsonString per half-chunk mangles it into U+FFFD.
func TestDepsLogChunkSplitUtf8(t *testing.T) {
	raw := []byte("已安装 3 个包")
	cut := len("已安装 3 ") + 1 // split 个 (3 bytes at [12..14]) after its first byte

	var d decodeChunk
	gotA, gotB := d.decode(raw[:cut]), d.decode(raw[cut:])
	if joined := gotA + gotB; joined != string(raw) {
		t.Fatalf("split decode = %q, want %q", joined, string(raw))
	}

	// This is the whole point: each 512-byte read becomes its own jsonString
	// payload appended to the depsBox. Each payload is a complete JS string
	// literal, so strip the outer quotes from both and the fragments must
	// spell out the whole escaped text.
	unquote := func(s string) string { return strings.Trim(s, `"`) }
	split := unquote(jsonString(gotA)) + unquote(jsonString(gotB))
	want := unquote(jsonString(string(raw)))
	if split != want {
		t.Fatalf("split chunks encoded to %q, want %q", split, want)
	}
	if strings.Contains(split, `\ufffd`) {
		t.Fatalf("replacement characters leaked into the log payload: %q", split)
	}

	// Regression guard: without decodeChunk the first fragment is mojibake.
	if unguarded := jsonString(string(raw[:cut])); !strings.Contains(unguarded, `\ufffd`) {
		t.Fatalf("expected the raw half-chunk to mangle to U+FFFD, got %s", unguarded)
	}
}

// --- portable command rewriting ----------------------------------------------

func writePkg(t *testing.T, dir, name string, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// "pnpm dsh web" → script "dsh" = "node --import tsx/esm apps/cli/src/bin.ts"
func TestRewriteCommandForPortableScriptWithArgs(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "package.json", `{"scripts":{"dsh":"node --import tsx/esm apps/cli/src/bin.ts"}}`)
	got, ok := rewriteCommandForPortable([]string{"pnpm", "dsh", "web"}, dir).([]string)
	if !ok {
		t.Fatalf("rewrite did not return []string: %v", rewriteCommandForPortable([]string{"pnpm", "dsh", "web"}, dir))
	}
	want := []string{"node", "--import", "tsx/esm", "apps/cli/src/bin.ts", "web"}
	if len(got) != len(want) {
		t.Fatalf("rewrite = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rewrite = %v, want %v", got, want)
		}
	}
}

// "pnpm run dev" with vite: resolves the real JS entry from node_modules.
func TestRewriteCommandForPortableVite(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "package.json", `{"scripts":{"dev":"vite"}}`)
	writePkg(t, dir, "node_modules/vite/package.json", `{"bin":{"vite":"bin/vite.js"}}`)
	writePkg(t, dir, "node_modules/vite/bin/vite.js", "// vite")
	got, ok := rewriteCommandForPortable([]string{"pnpm", "run", "dev"}, dir).([]string)
	if !ok {
		t.Fatalf("rewrite did not return []string")
	}
	want := []string{"node", filepath.Join("node_modules", "vite", "bin", "vite.js")}
	if len(got) != len(want) || got[0] != want[0] || filepath.ToSlash(got[1]) != filepath.ToSlash(want[1]) {
		t.Fatalf("rewrite = %v, want %v", got, want)
	}
}

// A script that is already runtime-first must pass through untouched.
func TestRewriteCommandForPortableDirectNode(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "package.json", `{"scripts":{"start":"node server.js"}}`)
	got, ok := rewriteCommandForPortable([]string{"node", "server.js"}, dir).([]string)
	if !ok {
		t.Fatalf("rewrite did not return []string")
	}
	want := []string{"node", "server.js"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rewrite = %v, want %v", got, want)
	}
}
