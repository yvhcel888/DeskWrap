package main

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func writeBuildSecurityFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireBuildSecurityMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not be packaged (Lstat error: %v)", path, err)
	}
}

func createBuildSecurityDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	// Creating symlinks can require Developer Mode on Windows. Junctions do
	// not, and exercise the same directory-expansion path.
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput(); err == nil {
			return
		} else {
			t.Logf("junction fallback failed: %v: %s", err, output)
		}
	}
	t.Skipf("directory links are unavailable: %s -> %s", link, target)
}

func createBuildSecurityFileLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlinks are unavailable: %s -> %s: %v", link, target, err)
	}
}

func TestCopyProjectDirExcludesSecretsThroughLinks(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	dst := t.TempDir()

	secrets := []string{
		".env",
		".env.local",
		".env.production",
		".npmrc",
		".pypirc",
		".netrc",
		"deskwrap.config.json",
		"deskwrap.config.local.json",
		"deskwrap.local.json",
	}
	for _, name := range secrets {
		writeBuildSecurityFile(t, filepath.Join(target, name), "secret-"+name)
	}
	writeBuildSecurityFile(t, filepath.Join(target, "keep.js"), "package code")
	createBuildSecurityDirLink(t, target, filepath.Join(src, "linked"))

	// Dependency build directories contain real package code and must remain.
	for _, name := range []string{"dist", "build", "out", "release"} {
		writeBuildSecurityFile(t, filepath.Join(src, "node_modules", "example", name, "index.js"), "dependency code")
	}
	writeBuildSecurityFile(t, filepath.Join(src, "node_modules", "example", ".npmrc"), "registry token")

	if err := copyProjectDir(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep.js"} {
		if _, err := os.Stat(filepath.Join(dst, "linked", name)); err != nil {
			t.Fatalf("linked package file %s was not copied: %v", name, err)
		}
	}
	for _, name := range secrets {
		requireBuildSecurityMissing(t, filepath.Join(dst, "linked", name))
	}
	requireBuildSecurityMissing(t, filepath.Join(dst, "node_modules", "example", ".npmrc"))
	for _, name := range []string{"dist", "build", "out", "release"} {
		if _, err := os.Stat(filepath.Join(dst, "node_modules", "example", name, "index.js")); err != nil {
			t.Fatalf("dependency code in %s was not copied: %v", name, err)
		}
	}
}

func TestCopyProjectDirSkipsOutputThroughLinks(t *testing.T) {
	src := t.TempDir()
	outDir := filepath.Join(src, "artifacts")
	dst := t.TempDir() // Keep the old unfiltered recursion bounded in the red run.
	writeBuildSecurityFile(t, filepath.Join(src, "keep.js"), "package code")
	writeBuildSecurityFile(t, filepath.Join(outDir, "stale.txt"), "old artifact")
	createBuildSecurityDirLink(t, outDir, filepath.Join(src, "linked-output"))

	if err := copyProjectDir(src, dst, outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "keep.js")); err != nil {
		t.Fatalf("project code was not copied: %v", err)
	}
	requireBuildSecurityMissing(t, filepath.Join(dst, "linked-output", "stale.txt"))
}

func TestCopyProjectDirExcludesSecretFileLinks(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	dst := t.TempDir()
	writeBuildSecurityFile(t, filepath.Join(target, "keep.js"), "package code")
	writeBuildSecurityFile(t, filepath.Join(target, ".env.local"), "secret")

	// Check both a secret link name and an innocuous name targeting a secret.
	createBuildSecurityFileLink(t, filepath.Join(target, "keep.js"), filepath.Join(src, ".env.alias"))
	createBuildSecurityFileLink(t, filepath.Join(target, ".env.local"), filepath.Join(src, "innocent.txt"))
	createBuildSecurityFileLink(t, filepath.Join(target, "keep.js"), filepath.Join(src, "safe.js"))

	if err := copyProjectDir(src, dst); err != nil {
		t.Fatal(err)
	}
	requireBuildSecurityMissing(t, filepath.Join(dst, ".env.alias"))
	requireBuildSecurityMissing(t, filepath.Join(dst, "innocent.txt"))
	if got, err := os.ReadFile(filepath.Join(dst, "safe.js")); err != nil || string(got) != "package code" {
		t.Fatalf("safe file link was not copied: content=%q, error=%v", got, err)
	}
}

func TestCopyProjectDirSkipsOutputFileLink(t *testing.T) {
	src := t.TempDir()
	outDir := filepath.Join(src, "artifacts")
	dst := t.TempDir()
	writeBuildSecurityFile(t, filepath.Join(outDir, "stale.txt"), "old artifact")
	createBuildSecurityFileLink(t, filepath.Join(outDir, "stale.txt"), filepath.Join(src, "artifact.txt"))

	if err := copyProjectDir(src, dst, outDir); err != nil {
		t.Fatal(err)
	}
	requireBuildSecurityMissing(t, filepath.Join(dst, "artifact.txt"))
}

func TestCopyProjectDirStopsDirectoryLinkCycles(t *testing.T) {
	src := t.TempDir()
	target := t.TempDir()
	dst := t.TempDir()
	writeBuildSecurityFile(t, filepath.Join(target, "keep.js"), "package code")
	createBuildSecurityDirLink(t, src, filepath.Join(target, "back-to-source"))
	createBuildSecurityDirLink(t, target, filepath.Join(src, "linked"))

	if err := copyProjectDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "linked", "keep.js")); err != nil {
		t.Fatalf("linked package code was not copied: %v", err)
	}
	requireBuildSecurityMissing(t, filepath.Join(dst, "linked", "back-to-source"))
}

func TestDoBuildRemovesStalePackagedSecrets(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "package-output")
	writeBuildSecurityFile(t, filepath.Join(outDir, "app", ".env"), "stale-secret")
	writeBuildSecurityFile(t, filepath.Join(dir, "main.js"), "console.log('ok')\n")
	cfg := map[string]any{
		"appName": "Stale Output",
		"outDir":  outDir,
		"service": map[string]any{
			"command": []any{"cmd.exe", "/c", "exit", "0"},
			"cwd":     dir,
		},
	}
	result := doBuild(dir, cfg)
	if !result.ok {
		t.Fatalf("doBuild failed: %s", result.err)
	}
	requireBuildSecurityMissing(t, filepath.Join(outDir, "app", ".env"))
}

func TestDoBuildRejectsOutputContainingSource(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "source.txt")
	writeBuildSecurityFile(t, marker, "source must survive")
	cfg := map[string]any{
		"appName": "Unsafe Output",
		"outDir":  parent,
		"service": map[string]any{
			"command": []any{"cmd.exe", "/c", "exit", "0"},
			"cwd":     dir,
		},
	}
	result := doBuild(dir, cfg)
	if result.ok {
		t.Fatal("doBuild accepted app/ as both source and destination")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "source must survive" {
		t.Fatalf("source was damaged: content=%q err=%v", got, err)
	}
}

func TestDoBuildRejectsSourceThroughLinkedOutput(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "source.txt")
	writeBuildSecurityFile(t, marker, "source must survive")
	alias := filepath.Join(t.TempDir(), "output-alias")
	createBuildSecurityDirLink(t, parent, alias)
	cfg := map[string]any{
		"appName": "Linked Output",
		"outDir":  alias,
		"service": map[string]any{
			"command": []any{"cmd.exe", "/c", "exit", "0"},
			"cwd":     dir,
		},
	}
	result := doBuild(dir, cfg)
	if result.ok {
		t.Fatal("doBuild accepted a linked output directory containing the source")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "source must survive" {
		t.Fatalf("source was damaged through linked output: content=%q err=%v", got, err)
	}
}

func TestDoBuildRejectsProjectAsOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "app", "source.txt")
	writeBuildSecurityFile(t, marker, "source must survive")
	cfg := map[string]any{
		"appName": "Project Output",
		"outDir":  dir,
		"service": map[string]any{
			"command": []any{"cmd.exe", "/c", "exit", "0"},
			"cwd":     dir,
		},
	}
	result := doBuild(dir, cfg)
	if result.ok {
		t.Fatal("doBuild accepted the project root as its output directory")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "source must survive" {
		t.Fatalf("source app directory was damaged: content=%q err=%v", got, err)
	}
}

func TestZipDirProducesReadableArchive(t *testing.T) {
	base := t.TempDir()
	writeBuildSecurityFile(t, filepath.Join(base, "app", "main.js"), "package code")
	writeBuildSecurityFile(t, filepath.Join(base, "app", "asset.zip"), "project asset")
	writeBuildSecurityFile(t, filepath.Join(base, "old-release.zip"), "old archive")
	zipPath := filepath.Join(base, "test.zip")
	if err := zipDir(zipPath, base); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("zip archive is not readable: %v", err)
	}
	defer r.Close()
	found := false
	foundAsset := false
	for _, f := range r.File {
		if f.Name == "app/main.js" {
			found = true
		}
		if f.Name == "app/asset.zip" {
			foundAsset = true
		}
		if f.Name == "test.zip" || f.Name == "old-release.zip" {
			t.Fatal("zip archive included another output archive")
		}
	}
	if !found {
		t.Fatal("zip archive omitted app/main.js")
	}
	if !foundAsset {
		t.Fatal("zip archive omitted a legitimate project ZIP asset")
	}
}

func TestDoBuildPreservesSourceConfig(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "package-output")
	sourcePath := filepath.Join(dir, "deskwrap.config.json")
	original := "{\n  \"ai\": {\"apiKey\": \"original-secret\"},\n  \"service\": {\"env\": {\"SOURCE_SECRET\": \"keep\"}}\n}\n"
	writeBuildSecurityFile(t, sourcePath, original)
	writeBuildSecurityFile(t, filepath.Join(dir, "main.js"), "console.log('ok')\n")

	cfg := map[string]any{
		"appName": "Security Regression",
		"outDir":  outDir,
		"ai":      map[string]any{"apiKey": "ai-secret"},
		"proxy":   map[string]any{"url": "https://proxy-secret.example"},
		"platformTokens": map[string]any{
			"example": "platform-secret",
		},
		"service": map[string]any{
			"command": []any{"cmd.exe", "/c", "exit", "0"},
			"cwd":     dir,
			"env":     map[string]any{"SERVICE_SECRET": "service-secret"},
		},
	}
	result := doBuild(dir, cfg)
	if !result.ok {
		t.Fatalf("doBuild failed: %s", result.err)
	}
	got, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("build modified the source config:\n%s", got)
	}
	requireBuildSecurityMissing(t, filepath.Join(outDir, "app", "deskwrap.config.json"))

	portable, err := readRawConfig(filepath.Join(outDir, "deskwrap.config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ai", "proxy", "platformTokens", "outDir"} {
		if _, ok := portable[key]; ok {
			t.Fatalf("portable config retained sensitive/local field %q", key)
		}
	}
	if env := cfgMap(portable, "service.env"); len(env) != 0 {
		t.Fatalf("portable config retained service.env: %v", env)
	}
}
