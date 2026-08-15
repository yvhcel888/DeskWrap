// DeskWrap - the build command.
//
// The radical simplification of the rewrite: packaging a project used to
// mean running electron-builder with a nested Electron runtime (a 10-minute
// 7z pass producing a 250MB artifact). Now the shell itself is a single
// small executable - "building" an app is: copy the shell, drop the config
// next to it, zip it up. No downloads, no toolchain, seconds instead of
// minutes, ~8MB instead of 250MB.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type buildResult struct {
	ok        bool
	err       string
	artifacts []string
}

// sanitizeName produces a filesystem-safe artifact name.
func sanitizeName(s string) string {
	replacer := strings.NewReplacer(
		`<`, "", `>`, "", `:`, "", `"`, "", `/`, "", `\`, "", `|`, "", `?`, "", `*`, "",
	)
	s = strings.TrimSpace(replacer.Replace(s))
	if s == "" {
		s = "DeskWrap"
	}
	return s
}

// doBuild packages the project at dir according to cfg.
// Returns artifact paths (the .exe and the .zip) on success.
func doBuild(dir string, cfg map[string]any) buildResult {
	buildLog.reset()

	// 1) Persist the config into the project (same as the original).
	target := filepath.Join(dir, "deskwrap.config.json")
	if err := writeRawConfig(target, cfg); err != nil {
		return buildResult{ok: false, err: "cannot write config: " + err.Error()}
	}

	// 2) Output directory.
	outDir := cfgStr(cfg, "outDir")
	if outDir == "" {
		outDir = filepath.Join(dir, "release")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return buildResult{ok: false, err: "cannot create output dir: " + err.Error()}
	}

	appName := cfgStr(cfg, "appName")
	if appName == "" {
		appName = filepath.Base(dir)
	}
	exeName := sanitizeName(appName)
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}

	exeSrc := mustExecutable()
	exeDst := filepath.Join(outDir, exeName)
	fmt.Printf("[DeskWrap] Copying shell %s -> %s\n", exeSrc, exeDst)
	if err := copyFile(exeSrc, exeDst); err != nil {
		return buildResult{ok: false, err: "cannot copy shell: " + err.Error()}
	}

	// 3) Sidecar DLL on Windows (WebView2 loader, 160KB).
	var sidecars []string
	if runtime.GOOS == "windows" {
		for _, cand := range []string{
			filepath.Join(filepath.Dir(exeSrc), "WebView2Loader.dll"),
			filepath.Join(mustRepoRoot(), "build", "WebView2Loader.dll"),
		} {
			if fileExists(cand) {
				dst := filepath.Join(outDir, "WebView2Loader.dll")
				if err := copyFile(cand, dst); err == nil {
					sidecars = append(sidecars, dst)
					break
				}
			}
		}
	}

	// 4) Config next to the exe (the shell reads it from its own directory).
	cfgDst := filepath.Join(outDir, "deskwrap.config.json")
	if err := copyFile(target, cfgDst); err != nil {
		return buildResult{ok: false, err: "cannot copy config: " + err.Error()}
	}

	// 5) Zip everything into <AppName>-win64.zip.
	zipPath := filepath.Join(outDir, sanitizeName(appName)+"-win64.zip")
	if err := zipArtifacts(zipPath, exeDst, cfgDst, sidecars); err != nil {
		return buildResult{ok: false, err: "cannot create zip: " + err.Error()}
	}

	buildLog.push(fmt.Sprintf("[DeskWrap] Built %s (%s)", exeName, zipPath))
	return buildResult{ok: true, artifacts: []string{exeDst, zipPath}}
}

func mustRepoRoot() string {
	// exeDir/.. only makes sense in the dev repo layout; for the packaged
	// app this just returns the exe directory - harmless.
	return filepath.Dir(filepath.Dir(mustExecutable()))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func zipArtifacts(zipPath, exe, cfg string, sidecars []string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	add := func(path, name string) error {
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, in)
		return err
	}

	if err := add(exe, filepath.Base(exe)); err != nil {
		return err
	}
	if err := add(cfg, "deskwrap.config.json"); err != nil {
		return err
	}
	for _, s := range sidecars {
		if err := add(s, filepath.Base(s)); err != nil {
			return err
		}
	}
	return nil
}
