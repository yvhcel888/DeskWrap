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
	"deskwrap.config.json": true,  // user config may contain API keys — only the portable config is shipped
	"release":              true,
	"dist":                 true,
	"__pycache__":          true, // Python bytecode caches (project source only)
	".next":                true,
	".nuxt":                true,
	".vs":                  true,
	".idea":                true,
	"build":                true,
	"out":                  true,
	".env":                 true,   // may contain secrets
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

// needsNodeRuntime reports whether the configured service command runs on
// Node (so the build bundles a portable Node.js).
func needsNodeRuntime(cfg map[string]any) bool {
	cmd, _ := resolveCommand(pickPath(cfg, "service.command"))
	if len(cmd) == 0 {
		return false
	}
	p := strings.ToLower(filepath.Base(cmd[0]))
	switch p {
	case "node", "node.exe", "npm", "npm.cmd", "npx", "npx.cmd", "pnpm", "pnpm.cmd", "yarn", "yarn.cmd":
		return true
	}
	return false
}

// needsPythonRuntime reports whether the configured service command runs on
// Python (so the build bundles a portable Python interpreter).
func needsPythonRuntime(cfg map[string]any) bool {
	cmd, _ := resolveCommand(pickPath(cfg, "service.command"))
	if len(cmd) == 0 {
		return false
	}
	p := strings.ToLower(filepath.Base(cmd[0]))
	return p == "python" || p == "python3" || p == "python.exe"
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
	if runtime.GOOS == "windows" {
		for _, cand := range []string{
			filepath.Join(filepath.Dir(exeSrc), "WebView2Loader.dll"),
			filepath.Join(mustRepoRoot(), "build", "WebView2Loader.dll"),
		} {
			if fileExists(cand) {
				dst := filepath.Join(outDir, "WebView2Loader.dll")
				if err := copyFile(cand, dst); err == nil {
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

	// 5) Bundle portable runtimes — the recipient doesn't need them installed.
	if runtime.GOOS == "windows" {
		if needsNodeRuntime(cfg) {
			if err := bundleNodeRuntime(outDir); err != nil {
				return buildResult{ok: false, err: "cannot bundle Node.js: " + err.Error()}
			}
		}
		// Python projects get a portable Python so the recipient never sees
		// a "pip install" hint.
		if needsPythonRuntime(cfg) {
			if err := bundlePythonRuntime(outDir, appDir); err != nil {
				return buildResult{ok: false, err: "cannot bundle Python: " + err.Error()}
			}
		}
	}

	// 6) Build a portable config: cwd relative, command rewritten for bundled node,
	//    and strip sensitive fields (API keys, proxy URLs, project-local paths).
	portableCfg := deepMerge(map[string]any{}, cfg)
	portableCfg["service"] = deepMerge(map[string]any{}, cfgMap(portableCfg, "service"))
	svcMap, _ := portableCfg["service"].(map[string]any)
	svcMap["cwd"] = "app"
	svcMap["command"] = rewriteCommandForPortable(svcMap["command"], appDir)
	// Strip user-specific / sensitive fields.
	delete(portableCfg, "ai")
	delete(portableCfg, "proxy")
	delete(portableCfg, "platformTokens")
	delete(portableCfg, "projectsDir")
	delete(portableCfg, "outDir") // local absolute path — meaningless on the recipient's machine
	svcMap["env"] = map[string]any{}
	cfgDst := filepath.Join(outDir, "deskwrap.config.json")
	if err := writeRawConfig(cfgDst, portableCfg); err != nil {
		return buildResult{ok: false, err: "cannot write portable config: " + err.Error()}
	}

	// 7) Zip: everything under outDir (exe + config + sidecars + app/ + node/ + python/).
	zipPath := filepath.Join(outDir, sanitizeName(appName)+"-win64.zip")
	if err := zipDir(zipPath, outDir); err != nil {
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
			if os.IsNotExist(err) { return nil } // skip missing dirs/files
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
		if buildExclude[name] {
			// Never apply the exclude list inside node_modules — those
			// directories (dist/, build/, etc.) contain actual package code.
			if !strings.Contains(filepath.ToSlash(rel), "node_modules/") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil // skip excluded files (deskwrap.config.json, .env, ...)
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

// bundlePythonRuntime builds a self-contained portable Python in
// outDir/python/ so packaged Python apps (Gradio/Flask/FastAPI/Streamlit)
// run on machines without Python installed — no "pip install" needed.
//
// Strategy: create a venv and pip-install the project's requirements into it
// (clean, no unrelated site-packages), then make it relocatable by copying the
// base interpreter's runtime (exe, DLLs, stdlib) next to the venv's
// site-packages and removing pyvenv.cfg. A plain python.exe (not the venv
// one) then treats the directory as its own home (home\Lib\os.py exists) and
// resolves site-packages relative to itself, so the whole folder moves freely.
func bundlePythonRuntime(outDir, appDir string) error {
	pythonPath, err := exec.LookPath("python")
	if err != nil {
		return nil // no python — skip (guidance page handles it)
	}
	baseDir := filepath.Dir(pythonPath)
	pyDir := filepath.Join(outDir, "python")

	// 1) venv: collects exactly the project's requirements.
	if err := runQuiet(pythonPath, "-m", "venv", pyDir); err != nil {
		return fmt.Errorf("cannot create venv: %w", err)
	}
	venvPy := filepath.Join(pyDir, "Scripts", "python.exe")
	installed := false
	req := filepath.Join(appDir, "requirements.txt")
	if fileExists(req) {
		fmt.Printf("[DeskWrap] Installing requirements into portable venv (pip install -r requirements.txt)...\n")
		if err := runQuiet(venvPy, "-m", "pip", "install", "--quiet", "-r", req); err == nil {
			installed = true
		} else {
			// Offline fallback: copy the base install's whole site-packages.
			fmt.Printf("[DeskWrap] pip install failed (%v) — copying base site-packages instead\n", err)
		}
	}
	if !installed {
		srcSP := filepath.Join(baseDir, "Lib", "site-packages")
		dstSP := filepath.Join(pyDir, "Lib", "site-packages")
		if dirExists(srcSP) {
			if err := copyDirTree(srcSP, dstSP); err != nil {
				return fmt.Errorf("cannot copy site-packages: %w", err)
			}
		}
	}

	// 2) Relocatability: plain interpreter + runtime files + stdlib.
	if err := copyFile(filepath.Join(baseDir, "python.exe"), filepath.Join(pyDir, "python.exe")); err != nil {
		return fmt.Errorf("cannot copy python.exe: %w", err)
	}
	if src := filepath.Join(baseDir, "pythonw.exe"); fileExists(src) {
		_ = copyFile(src, filepath.Join(pyDir, "pythonw.exe"))
	}
	for _, f := range []string{"python3.dll", "python310.dll", "python311.dll", "python312.dll", "python313.dll", "python314.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		if src := filepath.Join(baseDir, f); fileExists(src) {
			if err := copyFile(src, filepath.Join(pyDir, f)); err != nil {
				return err
			}
		}
	}
	// stdlib (Lib/ minus site-packages) must exist so python.exe recognizes
	// pyDir as its home via home\Lib\os.py.
	srcLib := filepath.Join(baseDir, "Lib")
	dstLib := filepath.Join(pyDir, "Lib")
	if dirExists(srcLib) {
		if err := copyDirTreeExclude(srcLib, dstLib, map[string]bool{"site-packages": true, "__pycache__": true}); err != nil {
			return fmt.Errorf("cannot copy stdlib: %w", err)
		}
	}
	if src := filepath.Join(baseDir, "DLLs"); dirExists(src) {
		if err := copyDirTree(src, filepath.Join(pyDir, "DLLs")); err != nil {
			return fmt.Errorf("cannot copy DLLs: %w", err)
		}
	}
	// 3) Drop venv-only artifacts: pyvenv.cfg (absolute base path) and the
	// Scripts/ launchers (hard-coded shebangs).
	os.Remove(filepath.Join(pyDir, "pyvenv.cfg"))
	os.RemoveAll(filepath.Join(pyDir, "Scripts"))

	// 4) Strip bytecode caches (large dep trees can carry 50MB+ of __pycache__).
	filepath.Walk(pyDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Name() == "__pycache__" {
			os.RemoveAll(path)
			return filepath.SkipDir
		}
		return nil
	})

	fmt.Printf("[DeskWrap] Bundled portable Python %s -> %s\n", pythonPath, pyDir)
	return nil
}

// runQuiet runs a command and returns its error (output discarded).
func runQuiet(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// copyDirTree copies src to dst recursively, skipping __pycache__.
func copyDirTree(src, dst string) error {
	return copyDirTreeExclude(src, dst, map[string]bool{"__pycache__": true})
}

// copyDirTreeExclude copies src to dst recursively, skipping any directory
// whose base name is in exclude.
func copyDirTreeExclude(src, dst string, exclude map[string]bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) { return nil }
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if info.IsDir() {
			if exclude[filepath.Base(rel)] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), info.Mode())
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
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

// zipDir zips the whole output directory (exe, dlls, config, app/, node/,
// python/) so the recipient can unzip and double-click with nothing
// installed. The zip file itself is excluded.
func zipDir(zipPath, base string) error {
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

	return filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) { return nil }
			return err
		}
		if path == zipPath || info.IsDir() {
			return nil // skip the zip itself and directory entries (implied)
		}
		rel, _ := filepath.Rel(base, path)
		return add(path, filepath.ToSlash(rel))
	})
}
