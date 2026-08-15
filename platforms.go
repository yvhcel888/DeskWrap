// DeskWrap - open-source platform search & one-click deploy.
//
// Built-in platforms: GitHub / Gitee / HuggingFace / ModelScope / GitLab /
// Codeberg. Users can add their own via config.platforms. Each entry
// describes the search API and how to map results to a git-cloneable URL.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var defaultPlatforms = []map[string]any{
	{
		"id": "github", "name": "GitHub",
		"search":   "https://api.github.com/search/repositories?q={q}&per_page=20",
		"listPath": "items",
		"map":      map[string]any{"name": "full_name", "desc": "description", "clone": "clone_url", "url": "html_url", "stars": "stargazers_count"},
	},
	{
		"id": "gitee", "name": "Gitee 码云", "needsToken": true,
		"search":   "https://gitee.com/api/v5/search/repositories?q={q}&per_page=20&page=1",
		"listPath": "",
		"map":      map[string]any{"name": "full_name", "desc": "description", "clone": "html_url", "url": "html_url", "stars": "stargazers_count"},
	},
	{
		"id": "huggingface", "name": "HuggingFace",
		"search":      "https://huggingface.co/api/models?search={q}&limit=20&full=true",
		"listPath":    "",
		"map":         map[string]any{"name": "id", "desc": "pipeline_tag", "clone": "", "url": "", "stars": "downloads"},
		"clonePrefix": "https://huggingface.co/{name}",
	},
	{
		"id": "modelscope", "name": "魔搭 ModelScope", "needsToken": true,
		"search":      "https://modelscope.cn/api/v1/dolphin/models?PageSize=20&SearchWord={q}",
		"listPath":    "Model.Models",
		"map":         map[string]any{"name": "Name", "desc": "Description", "clone": "", "url": "", "stars": "Downloads"},
		"clonePrefix": "https://www.modelscope.cn/{name}.git",
	},
	{
		"id": "gitlab", "name": "GitLab",
		"search":   "https://gitlab.com/api/v4/projects?search={q}&per_page=20&order_by=stars",
		"listPath": "",
		"map":      map[string]any{"name": "path_with_namespace", "desc": "description", "clone": "http_url_to_repo", "url": "web_url", "stars": "star_count"},
	},
	{
		"id": "codeberg", "name": "Codeberg",
		"search":   "https://codeberg.org/api/v1/repos/search?q={q}&limit=20",
		"listPath": "data",
		"map":      map[string]any{"name": "full_name", "desc": "description", "clone": "clone_url", "url": "html_url", "stars": "stars_count"},
	},
}

func userPlatforms() []map[string]any {
	list := append([]map[string]any{}, defaultPlatforms...)
	cfgMu.RLock()
	var arr []any
	if a, ok := pickPath(config, "platforms").([]any); ok {
		arr = a
	}
	cfgMu.RUnlock()
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			list = append(list, m)
		}
	}
	return list
}

// platformToken resolves an auth token: the platform's own token field,
// config.platformTokens[id], or a <ID>_TOKEN / <ID>_API_TOKEN env var.
func platformToken(platformID string, plat map[string]any) string {
	if tok := fmt.Sprint(plat["token"]); tok != "" && tok != "<nil>" {
		return tok
	}
	cfgMu.RLock()
	tokens := cfgMap(config, "platformTokens")
	cfgMu.RUnlock()
	if stored := cfgStr(tokens, platformID); stored != "" {
		return stored
	}
	envName := strings.ToUpper(strings.ReplaceAll(platformID, "-", "_"))
	if v := os.Getenv(envName + "_TOKEN"); v != "" {
		return v
	}
	return os.Getenv(envName + "_API_TOKEN")
}

// pickPathAny walks a dotted path through nested maps.
func pickPathAny(m map[string]any, path string) any {
	return pickPath(m, path)
}

// resolveProjectInput resolves a user address (local path or git URL).
func resolveProjectInput(address string) map[string]any {
	input := strings.TrimSpace(address)
	if input == "" {
		return map[string]any{"ok": false, "error": t("emptyAddr")}
	}

	// MSYS-style path from git-bash: /d/foo -> D:\foo
	local := input
	if runtime.GOOS == "windows" {
		if m := regexp.MustCompile(`^/([a-zA-Z])/`).FindStringSubmatch(input); m != nil {
			local = strings.ToUpper(m[1]) + ":\\" + strings.ReplaceAll(input[3:], "/", "\\")
		}
	}
	if dirExists(local) {
		return map[string]any{"ok": true, "dir": local, "cloned": false}
	}

	// Git URL?
	if regexp.MustCompile(`^(https?|git|ssh)://`).MatchString(input) ||
		regexp.MustCompile(`^[\w.-]+@[\w.-]+:[\w./-]+\.git$`).MatchString(input) {
		cfgMu.RLock()
		projectsRoot := cfgStr(config, "projectsDir")
		proxy := cfgMap(config, "proxy")
		cfgMu.RUnlock()
		if projectsRoot == "" {
			home, _ := os.UserHomeDir()
			projectsRoot = filepath.Join(home, "DeskWrapProjects")
		}
		if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}

		name := strings.TrimSuffix(input, ".git")
		if i := strings.LastIndexAny(name, "/:"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" {
			name = "project"
		}
		target := filepath.Join(projectsRoot, name)
		if !dirExists(target) {
			args := []string{"clone", "--depth", "1"}
			if cfgBool(proxy, "enabled") && cfgStr(proxy, "url") != "" {
				args = append(args, "-c", "http.proxy="+cfgStr(proxy, "url"), "-c", "https.proxy="+cfgStr(proxy, "url"))
			}
			args = append(args, input, target)
			c := exec.Command("git", args...)
			applyHiddenFlags(c)
			var out strings.Builder
			c.Stdout = &out
			c.Stderr = &out
			done := make(chan error, 1)
			go func() { done <- c.Run() }()
			select {
			case err := <-done:
				if err != nil {
					lines := nonEmptyLines(out.String())
					msg := strings.Join(lastN(lines, 3), " ")
					if msg == "" {
						msg = fmt.Sprintf("git clone 失败 (%v)", err)
					}
					serviceLog.push(fmt.Sprintf("[%s] %s: %s", t("cloneFailed"), input, msg))
					return map[string]any{"ok": false, "error": msg}
				}
			case <-time.After(10 * time.Minute):
				killProcessTree(c.Process.Pid)
				return map[string]any{"ok": false, "error": "git clone 超时（10 分钟）"}
			}
		}
		return map[string]any{"ok": true, "dir": target, "cloned": true}
	}

	return map[string]any{"ok": false, "error": t("unknownAddr") + ": " + input}
}

func nonEmptyLines(s string) []string {
	raw := strings.Split(s, "\n")
	out := []string{}
	for _, l := range raw {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func lastN(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// platformSearch queries a platform's search API and maps the results.
func platformSearch(platformID, query string) map[string]any {
	var plat map[string]any
	for _, p := range userPlatforms() {
		if fmt.Sprint(p["id"]) == platformID {
			plat = p
			break
		}
	}
	if plat == nil {
		return map[string]any{"ok": false, "error": "未知平台"}
	}
	token := platformToken(platformID, plat)
	searchURL := strings.ReplaceAll(fmt.Sprint(plat["search"]), "{q}", url.QueryEscape(query))
	headers := map[string]string{
		"User-Agent": "DeskWrap/2.0",
		"Accept":     "application/json",
	}
	if token != "" {
		if platformID == "modelscope" || fmt.Sprint(plat["authHeader"]) != "" && fmt.Sprint(plat["authHeader"]) != "<nil>" {
			headers["Authorization"] = "Bearer " + token
		} else {
			sep := "&"
			if !strings.Contains(searchURL, "?") {
				sep = "?"
			}
			searchURL += sep + "access_token=" + url.QueryEscape(token)
		}
	}

	data, status, err := appFetch("GET", searchURL, headers, nil, 30*time.Second)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if status < 200 || status >= 300 {
		return map[string]any{"ok": false, "error": fmt.Sprintf("HTTP %d（可能需要 Token）", status)}
	}
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return map[string]any{"ok": false, "error": "响应不是有效 JSON"}
	}

	var list []any
	if lp := fmt.Sprint(plat["listPath"]); lp != "" {
		if v := pickPath(root, lp); v != nil {
			if arr, ok := v.([]any); ok {
				list = arr
			}
		}
	} else {
		var arr []any
		if json.Unmarshal(data, &arr) == nil {
			list = arr
		}
	}

	pm := map[string]any{}
	if m, ok := plat["map"].(map[string]any); ok {
		pm = m
	}
	clonePrefix := fmt.Sprint(plat["clonePrefix"])
	items := []map[string]any{}
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := fmt.Sprint(pickPath(m, fmt.Sprint(pm["name"])))
		if name == "" || name == "<nil>" {
			continue
		}
		cloneURL := fmt.Sprint(pickPath(m, fmt.Sprint(pm["clone"])))
		if cloneURL == "<nil>" || cloneURL == "" {
			cloneURL = strings.ReplaceAll(clonePrefix, "{name}", name)
		}
		items = append(items, map[string]any{
			"name":     name,
			"desc":     truncate(fmt.Sprint(pickPath(m, fmt.Sprint(pm["desc"]))), 120),
			"stars":    pickPath(m, fmt.Sprint(pm["stars"])),
			"url":      fmt.Sprint(pickPath(m, fmt.Sprint(pm["url"]))),
			"cloneUrl": cloneURL,
		})
		if len(items) >= 20 {
			break
		}
	}
	return map[string]any{"ok": true, "items": items}
}
