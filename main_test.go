//go:build windows

// DeskWrap unit tests - pure logic pieces (shim parsing, JSON extraction,
// config merging, detection). GUI/WebView2 boot is covered separately.
package main

import (
	"os"
	"path/filepath"
	"strings"
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
