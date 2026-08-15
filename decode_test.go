//go:build windows

package main

import (
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
)

// GBK bytes of 无效参数 (CE DE D0 A7 B2 CE CA FD) — decodeChunk must fall
// back to the ANSI codepage when the chunk is not valid UTF-8.
func TestDecodeChunkGBK(t *testing.T) {
	gbk, err := hex.DecodeString("CEDED0A7B2CECAFD")
	if err != nil {
		t.Fatal(err)
	}
	var d decodeChunk
	if got := d.decode(gbk); got != "无效参数" {
		t.Fatalf("GBK decode = %q, want 无效参数", got)
	}
}

// Mixed chunk: UTF-8 line then GBK line.
func TestDecodeChunkMixed(t *testing.T) {
	gbk, _ := hex.DecodeString("CEDED0A7B2CECAFD")
	chunk := append([]byte("hello\n"), gbk...)
	var d decodeChunk
	got := d.decode(chunk)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "无效参数") {
		t.Fatalf("mixed decode = %q", got)
	}
}

// Real-world repro: icacls error text on a zh-CN system. Uses a temp dir so
// the test works on any machine.
func TestDecodeIcaclsError(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("icacls", dir, "/f")
	c.Dir = dir
	out, _ := c.CombinedOutput()
	t.Logf("raw bytes: %x", out)
	var d decodeChunk
	decoded := d.decode(out)
	t.Logf("decoded: %q", decoded)
	if strings.Contains(decoded, "\ufffd") {
		t.Fatalf("decoded output contains replacement chars (mojibake): %q", decoded)
	}
}
