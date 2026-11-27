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
	".git":                  true,
	".DS_Store":             true,
	"Thumbs.db":             true,
	"deskwrap-webview-data": true,
	"deskwrap.config.json":  true, // user config may contain API keys — only the portable config is shipped
	"release":               true,
	"dist":                  true,
	"__pycache__":           true, // Python bytecode caches (project source only)
	".next":                 true,
	".nuxt":                 true,
	".vs":                   true,
	".idea":                 true,
	"build":                 true,
	"out":                   true,
	".env":                  true, // may contain secrets
}

// Secret-bearing files are excluded even inside node_modules. Other
// buildExclude entries are not applied there because dist/, build/, out/, and
// release/ commonly contain actual dependency code.
var buildSecretExclude = map[string]bool{
	".npmrc":                     true,
	".npmrc.local":               true,
	".pypirc":                    true,
	".netrc":                     true,
	".git-credentials":           true,
	"deskwrap.config.json":       true,
	"deskwrap.config.local.json": true,
	"deskwrap.local.json":        true,
}

func isBuildSecretName(name string) bool {
	name = strings.ToLower(filepath.Base(filepath.Clean(name)))
	return name == ".env" || strings.HasPrefix(name, ".env.") || buildSecretExclude[name]
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

func pathContains(parent, child string) bool {
	parentAbs, parentErr := filepath.Abs(parent)
	childAbs, childErr := filepath.Abs(child)
	if parentErr != nil || childErr != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// doBuild packages the project at dir according to cfg.
// Returns artifact paths (the .exe and the .zip) on success.
func doBuild(dir string, cfg map[string]any) buildResult {
	buildLog.reset()

	// 1) Output directory.
	outDir := cfgStr(cfg, "outDir")
	if outDir == "" {
		outDir = filepath.Join(dir, "release")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return buildResult{ok: false, err: "cannot create output dir: " + err.Error()}
	}
	appDir := filepath.Join(outDir, "app")
	resolvedAppDir := resolveCopyPath(appDir)
	resolvedSource := resolveCopyPath(dir)
	resolvedOutDir := resolveCopyPath(outDir)
	if pathContains(resolvedOutDir, resolvedSource) || pathContains(resolvedAppDir, resolvedSource) {
		return buildResult{ok: false, err: "output directory must not contain the source project"}
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

	// 2) Sidecar DLL on Windows (WebView2 loader, 160KB).
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

	// 3) Copy the project directory into outDir/app/ (including node_modules).
	fmt.Printf("[DeskWrap] Copying project %s -> %s\n", dir, appDir)
	if err := os.RemoveAll(appDir); err != nil {
		return buildResult{ok: false, err: "cannot clean previous app directory: " + err.Error()}
	}
	if err := copyProjectDir(dir, appDir, outDir); err != nil {
		return buildResult{ok: false, err: "cannot copy project: " + err.Error()}
	}

	// 4) Bundle portable runtimes — the recipient doesn't need them installed.
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

	// 5) Build a portable config: cwd relative, command rewritten for bundled node,
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

	// 6) Zip: everything under outDir (exe + config + sidecars + app/ + node/ + python/).
	zipPath := filepath.Join(outDir, sanitizeName(appName)+"-win64.zip")
	if err := zipDir(zipPath, outDir); err != nil {
		return buildResult{ok: false, err: "cannot create zip: " + err.Error()}
	}

	buildLog.push(fmt.Sprintf("[DeskWrap] Built %s (%s)", exeName, zipPath))
	return buildResult{ok: true, artifacts: []string{exeDst, zipPath}}
}

// rewriteCommandForPortable rewrites the service command to use the bundled
// node.exe directly instead of requiring pnpm/npm on the target machine.
// Handles "pnpm run dev", "pnpm dev", "pnpm dsh web" (script + extra args),
// and scripts whose body starts with node/python or a bin tool like tsx:
//
//	["node", "--import", "tsx/esm", "apps/cli/src/bin.ts", "web"]
func rewriteCommandForPortable(cmd any, appDir string) any {
	parts, _ := resolveCommand(cmd)
	if len(parts) < 2 {
		return cmd
	}
	prog := strings.ToLower(filepath.Base(parts[0]))
	switch prog {
	case "node", "node.exe", "python", "python.exe", "python3":
		return cmd // uses the runtime directly — startService points at the bundled one
	case "pnpm", "pnpm.cmd", "npm", "npm.cmd", "yarn", "yarn.cmd":
		var scriptName string
		var restArgs []string
		if parts[1] == "run" && len(parts) >= 3 {
			scriptName, restArgs = parts[2], parts[3:]
		} else if len(parts) >= 2 {
			scriptName, restArgs = parts[1], parts[2:]
		}
		pkgPath := filepath.Join(appDir, "package.json")
		if pkg, err := readRawConfig(pkgPath); err == nil {
			if scripts, ok := pkg["scripts"].(map[string]any); ok {
				if actual, ok := scripts[scriptName].(string); ok && actual != "" {
					if rw := rewriteScriptForPortable(actual, restArgs, appDir); rw != nil {
						return rw
					}
				}
			}
		}
		return cmd
	default:
		return cmd
	}
}

// rewriteScriptForPortable turns a package.json script body into a command
// that runs on the bundled runtime: node/python stay as-is (plus args), bin
// tools (tsx, vite, ...) are resolved to their real JS entry so "node <entry>"
// works without any shim. Returns nil if it cannot rewrite.
func rewriteScriptForPortable(actual string, restArgs []string, appDir string) []string {
	fields := strings.Fields(actual)
	if len(fields) == 0 {
		return nil
	}
	binary := strings.ToLower(fields[0])
	var entry string
	switch binary {
	case "node", "node.exe", "python", "python3", "python.exe":
		// Runtime-first script (e.g. "node --import tsx/esm apps/cli/src/bin.ts").
		out := append([]string{binary}, fields[1:]...)
		return append(out, restArgs...)
	default:
		entry = binEntry(appDir, fields[0])
		if entry == "" {
			// Common fallbacks for well-known tools.
			switch binary {
			case "tsx":
				entry = "node_modules/tsx/dist/cli.mjs"
			case "vite":
				entry = "node_modules/vite/bin/vite.js"
			case "vitest":
				entry = "node_modules/vitest/vitest.mjs"
			}
			if entry != "" && fileExists(filepath.Join(appDir, entry)) {
				out := append([]string{"node", entry}, fields[1:]...)
				return append(out, restArgs...)
			}
			return nil
		}
	}
	out := append([]string{"node", entry}, fields[1:]...)
	return append(out, restArgs...)
}

// binEntry resolves the real JS entry of a bin tool installed in
// node_modules/<binary>/ (the .bin/ scripts are shell/batch wrappers that
// don't work with node).
func binEntry(appDir, binary string) string {
	binPkg := filepath.Join(appDir, "node_modules", binary, "package.json")
	bpkg, err := readRawConfig(binPkg)
	if err != nil {
		return ""
	}
	entry := ""
	if binMap, ok := bpkg["bin"].(map[string]any); ok {
		entry, _ = binMap[binary].(string)
	}
	if entry == "" {
		if s, ok := bpkg["bin"].(string); ok {
			entry = s
		}
	}
	if entry == "" {
		return ""
	}
	full := filepath.Join(appDir, "node_modules", binary, entry)
	if !fileExists(full) {
		return ""
	}
	return filepath.Join("node_modules", binary, entry)
}

// copyProjectDir copies the source tree to dst, skipping secret files,
// buildExclude entries outside node_modules, and the output directory itself
// (which may be inside the source tree). Directory links are expanded with the
// same rules rather than copied as unfiltered trees.
func copyProjectDir(src, dst string, skipDirs ...string) error {
	state, err := newCopyWalkState(skipDirs...)
	if err != nil {
		return err
	}
	state.exclude = func(name, rel string, isDir bool) bool {
		if isBuildSecretName(name) {
			return true
		}
		if copyPathInNodeModules(rel) {
			return false
		}
		return buildExclude[name]
	}
	state.secretName = isBuildSecretName
	return state.copyDir(src, dst, ".", 0)
}

func copyPathInNodeModules(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
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
	state := &copyWalkState{activeDirs: map[string]bool{}}
	if exclude != nil {
		state.exclude = func(name, rel string, isDir bool) bool {
			return isDir && exclude[name]
		}
	}
	return state.copyDir(src, dst, ".", 0)
}

// isJunctionDir reports whether path is a Windows junction (or directory
// symlink). Go's Lstat reports a junction as a plain file with an unknown
// mode ("?rw-rw-rw-"), while Stat follows it and sees a directory — that
// mismatch is the reliable detection.
func isJunctionDir(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.IsDir() {
		return false // normal dir or missing — handled elsewhere
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// copyWalkState gives ordinary and expanded directory copies one set of
// exclusion, output-directory, and cycle rules.
type copyWalkState struct {
	exclude    func(name, rel string, isDir bool) bool
	secretName func(name string) bool
	skipDirs   map[string]bool
	activeDirs map[string]bool
}

func newCopyWalkState(skipDirs ...string) (*copyWalkState, error) {
	state := &copyWalkState{
		skipDirs:   map[string]bool{},
		activeDirs: map[string]bool{},
	}
	for _, dir := range skipDirs {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		abs = filepath.Clean(abs)
		state.skipDirs[copyPathKey(abs)] = true
		state.skipDirs[copyPathKey(resolveCopyPath(abs))] = true
	}
	return state, nil
}

func copyPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func resolveCopyPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	current := abs
	var suffix []string
	for depth := 0; depth <= maxLinkDepth*2; depth++ {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil && copyPathKey(resolved) != copyPathKey(current) {
			current = resolved
			continue
		}
		target, err := os.Readlink(current)
		if err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			current = filepath.Clean(target)
			continue
		}
		parent := filepath.Dir(current)
		if parent == current {
			if len(suffix) > 0 {
				return filepath.Join(append([]string{current}, suffix...)...)
			}
			return current
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
	if len(suffix) > 0 {
		return filepath.Join(append([]string{current}, suffix...)...)
	}
	return current
}

func copyDirIdentity(path string) (string, error) {
	resolved := resolveCopyPath(path)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return copyPathKey(abs), nil
}

// pathSkipped also checks ancestors so a link directly to a file under outDir
// cannot bypass the directory skip.
func (s *copyWalkState) pathSkipped(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	check := func(path string) bool {
		for {
			if s.skipDirs[copyPathKey(path)] {
				return true
			}
			parent := filepath.Dir(path)
			if parent == path {
				return false
			}
			path = parent
		}
	}
	if check(filepath.Clean(abs)) {
		return true
	}
	return check(filepath.Clean(resolveCopyPath(abs)))
}

func (s *copyWalkState) copyDir(path, dst, rel string, depth int) error {
	// The depth limit bounds acyclic link chains; activeDirs stops actual
	// cycles immediately instead of duplicating them until the limit.
	if depth > maxLinkDepth || s.pathSkipped(path) {
		return nil
	}
	identity, err := copyDirIdentity(path)
	if err != nil {
		return err
	}
	if s.activeDirs[identity] {
		return nil
	}
	s.activeDirs[identity] = true
	defer delete(s.activeDirs, identity)

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(path, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		childRel := filepath.Join(rel, entry.Name())
		info, err := os.Lstat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := s.copyEntry(src, dstPath, childRel, info, depth); err != nil {
			return err
		}
	}
	return nil
}

func (s *copyWalkState) copyEntry(src, dst, rel string, info os.FileInfo, depth int) error {
	isLink := info.Mode()&os.ModeSymlink != 0 || isJunctionDir(src)
	targetInfo := info
	if isLink {
		var err error
		targetInfo, err = os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // broken link
			}
			return err
		}
		resolved := resolveCopyPath(src)
		if s.pathSkipped(resolved) {
			return nil
		}
		// An innocuous link name must not expose a credential file.
		if s.secretName != nil && s.secretName(filepath.Base(resolved)) {
			return nil
		}
	}
	isDir := targetInfo.IsDir()
	if s.exclude != nil && s.exclude(filepath.Base(src), rel, isDir) {
		return nil
	}
	if isDir {
		if isLink {
			depth++
		}
		return s.copyDir(src, dst, rel, depth)
	}
	return copyFile(src, dst) // os.Open follows file links
}

// copyExpanded copies the content of a link target (symlink or Windows
// junction) into dst, recursing into nested links. It is used instead of
// plain copyFile because a directory junction cannot be read like a file.
//
// depth guards against circular junction chains (pnpm stores A↔B circular
// deps as junctions that alternate forever). The active-directory set catches
// cycles; the depth limit also bounds duplicated content in long acyclic
// chains. 12 = 6 full cycles, ample headroom.
const maxLinkDepth = 12

func copyExpanded(path, dst string, exclude map[string]bool, depth int) error {
	state := &copyWalkState{activeDirs: map[string]bool{}}
	if exclude != nil {
		state.exclude = func(name, rel string, isDir bool) bool {
			return isDir && exclude[name]
		}
	}
	return state.copyDir(path, dst, ".", depth)
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
func zipDir(zipPath, base string) (err error) {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	zw := zip.NewWriter(f)
	zwClosed := false
	defer func() {
		if !zwClosed {
			_ = zw.Close()
		}
	}()

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

	walkErr := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == zipPath || info.IsDir() || (filepath.Dir(path) == base && strings.EqualFold(filepath.Ext(path), ".zip")) {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		name := filepath.ToSlash(rel)
		// Skip unreachable deep circular-duplicate entries (expanded junction
		// chains) — most extractors fail on paths beyond 260 chars.
		if len(name) > 250 {
			return nil
		}
		return add(path, name)
	})
	if walkErr != nil {
		return walkErr
	}
	if err := zw.Close(); err != nil {
		return err
	}
	zwClosed = true
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	return nil
}
