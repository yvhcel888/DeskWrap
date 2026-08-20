// DeskWrap - the build command.
//
// Packages the project for cross-machine distribution:
//   - Shell (exe + WebView2Loader.dll)  – the native launcher
//   - Project source (app/)             – the service code + node_modules
//   - Portable Node.js (node/)          – bundled runtime, recipient doesn't need it
//   - Config (deskwrap.config.json)     – service.cwd relativized, command rewritten
//
// "Building" = copy files + zip.  Seconds, not minutes; megabytes, not
// hundreds of megabytes.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type buildResult struct {
	ok        bool
	err       string
	artifacts []string
}

// buildExclude patterns are excluded when copying the project directory.
// Note: node_modules is intentionally NOT excluded — the packaged app
// ships with dependencies pre-installed so the recipient can double-click
// and run without needing to install anything.
var buildExclude = map[string]bool{
	".git":                 true,
	".DS_Store":            true,
	"Thumbs.db":            true,
	"deskwrap-webview-data": true,
	"release":              true,
	"dist":                 true,
	"__pycache__":          true,
	".next":                true,
	".nuxt":                true,
	".vs":                  true,
	".idea":                true,
	"build":                true,
	"out":                  true,
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

	// 1) Persist the config into the project.
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

	// 4) Copy the project directory into outDir/app/ (including node_modules).
	appDir := filepath.Join(outDir, "app")
	fmt.Printf("[DeskWrap] Copying project %s -> %s\n", dir, appDir)
	if err := copyProjectDir(dir, appDir, outDir); err != nil {
		return buildResult{ok: false, err: "cannot copy project: " + err.Error()}
	}

	// 5) Bundle portable Node.js — the recipient doesn't need it installed.
	if runtime.GOOS == "windows" {
		if err := bundleNodeRuntime(outDir); err != nil {
			return buildResult{ok: false, err: "cannot bundle Node.js: " + err.Error()}
		}
	}

	// 6) Build a portable config: cwd relative, command rewritten for bundled node.
	portableCfg := deepMerge(map[string]any{}, cfg)
	portableCfg["service"] = deepMerge(map[string]any{}, cfgMap(portableCfg, "service"))
	svcMap, _ := portableCfg["service"].(map[string]any)
	svcMap["cwd"] = "app"
	svcMap["command"] = rewriteCommandForPortable(svcMap["command"], appDir)
	cfgDst := filepath.Join(outDir, "deskwrap.config.json")
	if err := writeRawConfig(cfgDst, portableCfg); err != nil {
		return buildResult{ok: false, err: "cannot write portable config: " + err.Error()}
	}

	// 7) Zip: exe + config + sidecars + app/ + node/.
	zipPath := filepath.Join(outDir, sanitizeName(appName)+"-win64.zip")
	zipFiles := []string{exeDst, cfgDst}
	zipFiles = append(zipFiles, sidecars...)
	if err := zipDir(zipPath, outDir, zipFiles); err != nil {
		return buildResult{ok: false, err: "cannot create zip: " + err.Error()}
	}

	buildLog.push(fmt.Sprintf("[DeskWrap] Built %s (%s)", exeName, zipPath))
	return buildResult{ok: true, artifacts: []string{exeDst, zipPath}}
}

// rewriteCommandForPortable rewrites the service command to use the bundled
// node.exe directly instead of requiring pnpm/npm on the target machine.
// For ["pnpm","run","dev"], it reads the "dev" script from package.json and
// returns ["node","node_modules/.bin/vite"] (or equivalent).
func rewriteCommandForPortable(cmd any, appDir string) any {
	parts, _ := resolveCommand(cmd)
	if len(parts) < 2 {
		return cmd
	}
	prog := strings.ToLower(filepath.Base(parts[0]))
	switch prog {
	case "node", "node.exe":
		return cmd // already uses node directly
	case "pnpm", "pnpm.cmd", "npm", "npm.cmd", "yarn", "yarn.cmd":
		scriptName := parts[len(parts)-1]
		if len(parts) >= 3 && parts[1] == "run" {
			scriptName = parts[2]
		}
		pkgPath := filepath.Join(appDir, "package.json")
		if pkg, err := readRawConfig(pkgPath); err == nil {
			if scripts, ok := pkg["scripts"].(map[string]any); ok {
				if actual, ok := scripts[scriptName].(string); ok && actual != "" {
					binary := strings.Fields(actual)[0]
					// Find the real JS entry point from node_modules/<binary>/package.json
					// (the .bin/ scripts are shell/batch wrappers that don't work with node).
					binPkg := filepath.Join(appDir, "node_modules", binary, "package.json")
					if bpkg, err2 := readRawConfig(binPkg); err2 == nil {
						if binMap, ok := bpkg["bin"].(map[string]any); ok {
							if entry, ok := binMap[binary].(string); ok {
								fullEntry := filepath.Join(appDir, "node_modules", binary, entry)
								if fileExists(fullEntry) {
									return []string{"node", filepath.Join("node_modules", binary, entry)}
								}
							}
						}
					}
					// Fallback: node -e with the script content (for code-like scripts).
					return []string{"node", "-e", actual}
				}
			}
		}
		return cmd
	default:
		return cmd
	}
}

// copyProjectDir copies the source tree to dst, skipping buildExclude entries
// and the output directory itself (which may be inside the source tree).
func copyProjectDir(src, dst string, skipDirs ...string) error {
	skip := map[string]bool{}
	for _, d := range skipDirs {
		skip[filepath.Clean(d)] = true
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if info.IsDir() && skip[filepath.Clean(path)] {
			return filepath.SkipDir
		}
		name := filepath.Base(rel)
		if info.IsDir() && buildExclude[name] {
			// Never apply the exclude list inside node_modules — those
			// directories (dist/, build/, etc.) contain actual package code.
			if !strings.Contains(filepath.ToSlash(rel), "node_modules/") {
				return filepath.SkipDir
			}
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		return copyFile(path, dstPath)
	})
}

// bundleNodeRuntime copies the system Node.js (node.exe + npm/) into
// outDir/node/ so the packaged app can run without Node installed on the
// target machine.
func bundleNodeRuntime(outDir string) error {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return nil // no node — skip (guidance page handles it)
	}
	nodeDir := filepath.Join(outDir, "node")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		return err
	}
	dstNode := filepath.Join(nodeDir, "node.exe")
	if err := copyFile(nodePath, dstNode); err != nil {
		return err
	}
	fmt.Printf("[DeskWrap] Bundled Node.js %s -> %s\n", nodePath, dstNode)
	npmDir := filepath.Join(filepath.Dir(nodePath), "node_modules", "npm")
	if dirExists(npmDir) {
		dstNpm := filepath.Join(nodeDir, "node_modules", "npm")
		if err := copyProjectDir(npmDir, dstNpm); err == nil {
			fmt.Printf("[DeskWrap] Bundled npm -> %s\n", dstNpm)
		}
	}
	return nil
}

func mustRepoRoot() string {
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

// zipDir adds files (by absolute path, relative to base) and the app/
// subdirectory to the zip archive.
func zipDir(zipPath, base string, extraFiles []string) error {
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

	for _, path := range extraFiles {
		rel, _ := filepath.Rel(base, path)
		if err := add(path, filepath.ToSlash(rel)); err != nil {
			return err
		}
	}
	// Add the app/ directory tree.
	appDir := filepath.Join(base, "app")
	return filepath.Walk(appDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		return add(path, filepath.ToSlash(rel))
	})
}
