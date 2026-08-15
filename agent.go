// DeskWrap - built-in ops agent + AI provider plumbing.
//
// A small, auditable agent loop: the configured LLM analyzes the state
// (logs, env, errors), proposes ONE action per turn as strict JSON, the
// executor runs it, and the result is fed back. Repeats until the goal is
// reached or the iteration budget (15) runs out. Six tools, no chaining,
// no arbitrary file access - only project-scoped operations.
//
// All AI traffic goes through appFetch (proxy-aware): the packaged app must
// respect the user's optional proxy, and Go's default HTTP client does not
// automatically pick up a GUI-set proxy URL.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxIterations = 15
	maxOutput     = 2000
)

// --- AI providers (OpenAI-compatible) --------------------------------------

type aiProvider struct {
	name  string
	base  string
	model string
	noKey bool
}

var aiProviders = map[string]aiProvider{
	"glm":         {"GLM (智谱)", "https://open.bigmodel.cn/api/paas/v4/chat/completions", "glm-4-flash", false},
	"deepseek":    {"DeepSeek", "https://api.deepseek.com/chat/completions", "deepseek-chat", false},
	"qwen":        {"通义千问 (阿里)", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", "qwen-plus", false},
	"openai":      {"OpenAI", "https://api.openai.com/v1/chat/completions", "gpt-4o-mini", false},
	"openrouter":  {"OpenRouter (聚合)", "https://openrouter.ai/api/v1/chat/completions", "openai/gpt-4o-mini", false},
	"gemini":      {"Google Gemini", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", "gemini-2.0-flash", false},
	"xai":         {"xAI (Grok)", "https://api.x.ai/v1/chat/completions", "grok-2-latest", false},
	"kimi":        {"Kimi (月之暗面)", "https://api.moonshot.cn/v1/chat/completions", "moonshot-v1-8k", false},
	"minimax":     {"MiniMax", "https://api.minimax.chat/v1/text/chatcompletion_v2", "abab6.5s-chat", false},
	"siliconflow": {"硅基流动 SiliconFlow", "https://api.siliconflow.cn/v1/chat/completions", "deepseek-ai/DeepSeek-V3", false},
	"doubao":      {"豆包 (火山方舟)", "https://ark.cn-beijing.volces.com/api/v3/chat/completions", "doubao-1-5-pro-32k-250115", false},
	"stepfun":     {"阶跃星辰 StepFun", "https://api.stepfun.com/v1/chat/completions", "step-1-8k", false},
	"baichuan":    {"百川 Baichuan", "https://api.baichuan-ai.com/v1/chat/completions", "Baichuan4", false},
	"groq":        {"Groq (超快推理)", "https://api.groq.com/openai/v1/chat/completions", "llama-3.1-8b-instant", false},
	"mistral":     {"Mistral", "https://api.mistral.ai/v1/chat/completions", "mistral-small-latest", false},
	"fireworks":   {"Fireworks AI", "https://api.fireworks.ai/inference/v1/chat/completions", "accounts/fireworks/models/llama-v3p1-8b-instruct", false},
	"nvidia":      {"NVIDIA NIM", "https://integrate.api.nvidia.com/v1/chat/completions", "meta/llama-3.1-8b-instruct", false},
	"deepinfra":   {"DeepInfra", "https://api.deepinfra.com/v1/openai/chat/completions", "meta-llama/Meta-Llama-3.1-8B-Instruct", false},
	"huggingface": {"Hugging Face (router)", "https://router.huggingface.co/v1/chat/completions", "Qwen/Qwen2.5-72B-Instruct", false},
	"upstage":     {"Upstage Solar", "https://api.upstage.ai/v1/solar/chat/completions", "solar-pro", false},
	"novita":      {"Novita AI", "https://api.novita.ai/v3/openai/chat/completions", "deepseek/deepseek-v3", false},
	"xiaomi":      {"小米 MiMo", "https://api.xiaomi.com/v1/chat/completions", "MiMo-7B-RL", false},
	"ollama":      {"Ollama (本地)", "http://127.0.0.1:11434/v1/chat/completions", "qwen2.5", true},
	"custom":      {"自定义 (OpenAI 兼容)", "", "", true},
}

func providerOr(p string) aiProvider {
	if prov, ok := aiProviders[p]; ok {
		return prov
	}
	return aiProviders["glm"]
}

// getAiConfig resolves the effective AI settings (defaults from the provider
// table, key from config or a <PROVIDER>_API_KEY environment variable).
func getAiConfig() map[string]any {
	cfgMu.RLock()
	ai := cfgMap(config, "ai")
	cfgMu.RUnlock()
	provider := cfgStr(ai, "provider")
	prov := providerOr(provider)
	base := cfgStr(ai, "baseUrl")
	if base == "" {
		base = prov.base
	}
	model := cfgStr(ai, "model")
	if model == "" {
		model = prov.model
	}
	apiKey := cfgStr(ai, "apiKey")
	if apiKey == "" {
		apiKey = os.Getenv(strings.ToUpper(provider) + "_API_KEY")
	}
	return map[string]any{
		"enabled":  cfgBool(ai, "enabled"),
		"provider": provider,
		"base":     base,
		"model":    model,
		"apiKey":   apiKey,
		"noKey":    prov.noKey,
	}
}

// --- proxy-aware HTTP -------------------------------------------------------

// newHTTPClient builds a client that honors config.proxy.enabled or the
// standard HTTP(S)_PROXY / ALL_PROXY environment variables.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	cfgMu.RLock()
	proxy := cfgMap(config, "proxy")
	cfgMu.RUnlock()
	if cfgBool(proxy, "enabled") {
		if raw := cfgStr(proxy, "url"); raw != "" {
			if u, err := url.Parse(raw); err == nil {
				transport.Proxy = http.ProxyURL(u)
			}
		}
	}
	return &http.Client{Transport: transport}
}

// appFetch performs a request through the proxy-aware client with a timeout.
func appFetch(method, urlStr string, headers map[string]string, body []byte, timeout time.Duration) ([]byte, int, error) {
	client := newHTTPClient()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func jsonBody(m map[string]any) []byte {
	b, _ := json.Marshal(m)
	return b
}

// callChat sends an OpenAI-compatible chat request and returns the content.
func callChat(base, model, apiKey string, messages []map[string]any, timeout time.Duration, temperature float64, maxTokens int) (string, error) {
	body := jsonBody(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temperature,
		"max_tokens":  maxTokens,
	})
	headers := map[string]string{"Content-Type": "application/json"}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	data, status, err := appFetch("POST", base, headers, body, timeout)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("API error %d: %s", status, truncate(string(data), 300))
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("bad response: %w", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("empty reply")
	}
	return resp.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// aiSettingsFromOpts resolves the effective AI settings for GUI-triggered
// actions: the configured settings overridden by the GUI form values (the
// same values aiTest uses). The user may have typed a key and only run the
// connection test without saving, so diagnosis and the agent must honor the
// form just like the test does.
func aiSettingsFromOpts(opts map[string]any) map[string]any {
	cfg := getAiConfig()
	if opts == nil {
		return cfg
	}
	provider := fmt.Sprint(orDefault(opts["provider"], fmt.Sprint(cfg["provider"])))
	prov := providerOr(provider)
	// base/model: explicit form value > config value (only when the config
	// was resolved for the same provider) > provider default.
	base := fmt.Sprint(orDefault(opts["baseUrl"], ""))
	if base == "" && fmt.Sprint(cfg["provider"]) == provider {
		base = fmt.Sprint(cfg["base"])
	}
	if base == "" {
		base = prov.base
	}
	model := fmt.Sprint(orDefault(opts["model"], ""))
	if model == "" && fmt.Sprint(cfg["provider"]) == provider {
		model = fmt.Sprint(cfg["model"])
	}
	if model == "" {
		model = prov.model
	}
	key := fmt.Sprint(orDefault(opts["apiKey"], ""))
	if key == "" {
		key = fmt.Sprint(cfg["apiKey"])
	}
	enabled := cfgBool(cfg, "enabled")
	if v, ok := opts["enabled"].(bool); ok {
		enabled = v
	}
	return map[string]any{
		"enabled":  enabled,
		"provider": provider,
		"base":     base,
		"model":    model,
		"apiKey":   key,
		"noKey":    prov.noKey,
	}
}

// aiDiagnose analyzes the service log with the configured provider.
// A key is enough to diagnose; the "enabled" flag only gates the agent.
// Optional opts (GUI form values) override the configured settings.
func aiDiagnose(opts ...map[string]any) map[string]any {
	var ai map[string]any
	if len(opts) > 0 && opts[0] != nil {
		ai = aiSettingsFromOpts(opts[0])
	} else {
		ai = getAiConfig()
	}
	if ai["apiKey"] == "" && !ai["noKey"].(bool) {
		return map[string]any{"ok": false, "error": "未找到 API Key（请先在 AI 设置中填写 Key）"}
	}

	cfgMu.RLock()
	service := cfgMap(config, "service")
	proxy := cfgMap(config, "proxy")
	cfgMu.RUnlock()

	logTail := strings.Join(serviceLog.tail(150), "\n")
	if logTail == "" {
		logTail = "（暂无服务日志）"
	}
	cmdJSON, _ := json.Marshal(service["command"])
	port := cfgInt(service, "port")

	system := "你是 DeskWrap 桌面壳的内置运维助手。用户把本地 Web 服务包成桌面应用，" +
		"服务启动或运行出现问题。请分析下面提供的启动命令、配置和服务日志，" +
		"用中文给出：1) 最可能的错误原因（按概率排序）；2) 具体的解决步骤；" +
		"3) 如果问题与端口、命令、代理、依赖缺失相关，给出可直接套用的修复建议。" +
		"回答要精炼，使用列表，不要客套。"
	user := fmt.Sprintf("启动命令: %s\n端口: %d\n代理: %s\n\n服务日志（最近）:\n%s",
		string(cmdJSON), port, jsonString(proxy), logTail)

	content, err := callChat(
		fmt.Sprint(ai["base"]), fmt.Sprint(ai["model"]), fmt.Sprint(ai["apiKey"]),
		[]map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		60*time.Second, 0.3, 1024,
	)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "content": content}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// aiTest checks the provider connection with the given settings.
func aiTest(opts map[string]any) map[string]any {
	provider := fmt.Sprint(orDefault(opts["provider"], "glm"))
	prov := providerOr(provider)
	base := fmt.Sprint(orDefault(opts["baseUrl"], ""))
	if base == "" {
		base = prov.base
	}
	model := fmt.Sprint(orDefault(opts["model"], ""))
	if model == "" {
		model = prov.model
	}
	key := fmt.Sprint(orDefault(opts["apiKey"], ""))
	if key == "" {
		key = fmt.Sprint(getAiConfig()["apiKey"])
	}
	if !prov.noKey && key == "" {
		return map[string]any{"ok": false, "error": "未填写 API Key"}
	}
	if base == "" {
		return map[string]any{"ok": false, "error": "未填写 API 地址 (base URL)"}
	}
	content, err := callChat(base, model, key,
		[]map[string]any{{"role": "user", "content": "reply OK"}},
		30*time.Second, 0, 8)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "model": model, "content": content}
}

// aiModels fetches the provider's /models list (best effort).
func aiModels(opts map[string]any) map[string]any {
	provider := fmt.Sprint(orDefault(opts["provider"], "glm"))
	prov := providerOr(provider)
	base := fmt.Sprint(orDefault(opts["baseUrl"], ""))
	if base == "" {
		base = prov.base
	}
	key := fmt.Sprint(orDefault(opts["apiKey"], ""))
	if key == "" {
		key = fmt.Sprint(getAiConfig()["apiKey"])
	}
	if base == "" {
		return map[string]any{"ok": false, "error": "无 API 地址"}
	}
	root := regexp.MustCompile(`/chat/completions/?$`).ReplaceAllString(base, "")
	root = regexp.MustCompile(`/v\d+/?$`).ReplaceAllString(root, "")
	candidates := []string{root + "/models", strings.TrimSuffix(base, "/chat/completions") + "/models"}
	lastErr := ""
	for _, u := range candidates {
		headers := map[string]any{}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
		data, status, err := appFetch("GET", u, map[string]string{}, nil, 15*time.Second)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if status < 200 || status >= 300 {
			lastErr = fmt.Sprintf("HTTP %d", status)
			continue
		}
		var parsed struct {
			Data []map[string]any `json:"data"`
		}
		models := []string{}
		if json.Unmarshal(data, &parsed) == nil && len(parsed.Data) > 0 {
			for _, m := range parsed.Data {
				if id, ok := m["id"].(string); ok && id != "" {
					models = append(models, id)
				} else if name, ok := m["name"].(string); ok && name != "" {
					models = append(models, name)
				}
			}
		} else {
			var arr []any
			if json.Unmarshal(data, &arr) == nil {
				for _, m := range arr {
					switch v := m.(type) {
					case string:
						models = append(models, v)
					case map[string]any:
						if id, ok := v["id"].(string); ok && id != "" {
							models = append(models, id)
						}
					}
				}
			}
		}
		sortStrings(models)
		if len(models) > 0 {
			return map[string]any{"ok": true, "models": models}
		}
		lastErr = "无模型数据"
	}
	return map[string]any{"ok": false, "error": "无法获取模型列表: " + lastErr}
}

func orDefault(v any, def string) any {
	if v == nil {
		return def
	}
	return v
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- agent loop -------------------------------------------------------------

var agentStopped atomic.Bool

const agentSystemPrompt = `你是 DeskWrap 桌面工具的运维智能体。
当前任务目标：%s

环境信息：
- 操作系统: %s
- 项目目录: %s
- 服务命令: %s
- 端口: %d
- 环境: %s

你可以调用以下工具，每次回复必须是一个严格的 JSON 对象（不要 markdown 围栏、不要多余文字）：
1. {"action":"run_command","args":{"command":"<shell命令>","cwd":"<可选工作目录>"}}
   执行 shell 命令（如安装依赖 pnpm install、跑构建脚本），返回输出尾部。禁止危险命令（格式化磁盘、删除系统目录等）。
2. {"action":"write_config","args":{"patch":{"service":{"command":["npm","run","dev"],"port":3000}}}}
   合并修改当前项目的 deskwrap.config.json。改完通常需要重试 build 或 run_service。
3. {"action":"read_log","args":{}}
   读取服务日志与构建日志尾部，用于定位报错。
4. {"action":"check_env","args":{}}
   检查 Node/npm/pnpm/yarn/git/python 是否可用。
5. {"action":"build","args":{}}
   执行桌面应用打包（可能耗时数分钟）。
6. {"action":"run_service","args":{}}
   停止旧服务后按当前配置重新启动服务并打开窗口。

工作方式：
- 先 check_env / read_log 了解现状，定位问题根因；
- 每次只做一个动作，根据结果决定下一步；
- 依赖没装就先 run_command 安装；端口冲突就 write_config 换端口；代理问题就 write_config 调整 proxy；
- 当目标达成时回复 {"done":true,"success":true,"summary":"<完成说明>"}；
- 如果无法解决（缺权限、网络不通等），回复 {"done":true,"success":false,"summary":"<失败原因与建议>"}；
- 不要放弃得太快，多尝试不同修复路径。`

// parseAction extracts the first {...} JSON block from an LLM reply
// (tolerates markdown fences and surrounding text).
func parseAction(text string) map[string]any {
	cleaned := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", ""))
	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start == -1 || end <= start {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &m); err != nil {
		return nil
	}
	return m
}

type agentOpts struct {
	goal        string
	ai          map[string]any
	ctx         map[string]any
	executor    func(action string, args map[string]any) map[string]any
	goalReached func() bool
	onEvent     func(ev map[string]any)
	stopFlag    func() bool
}

func runAgent(opts agentOpts) map[string]any {
	messages := []map[string]any{
		{
			"role": "system",
			"content": fmt.Sprintf(agentSystemPrompt,
				opts.goal, runtimeOS(), orEmpty(opts.ctx["dir"]),
				jsonString(opts.ctx["command"]), orInt(opts.ctx["port"]),
				jsonString(opts.ctx["env"])),
		},
		{"role": "user", "content": "开始执行任务。目标：" + opts.goal},
	}

	lastSummary := ""
	for i := 0; i < maxIterations; i++ {
		if opts.stopFlag != nil && opts.stopFlag() {
			return map[string]any{"ok": false, "summary": "用户停止了智能体", "iterations": i}
		}

		content, err := callChat(
			fmt.Sprint(opts.ai["base"]), fmt.Sprint(opts.ai["model"]), fmt.Sprint(opts.ai["apiKey"]),
			messages, 120*time.Second, 0.2, 1200)
		if err != nil {
			opts.onEvent(map[string]any{"type": "error", "content": "AI 调用失败: " + err.Error()})
			return map[string]any{"ok": false, "summary": "AI 调用失败: " + err.Error(), "iterations": i}
		}

		action := parseAction(content)
		if action == nil {
			opts.onEvent(map[string]any{"type": "ai", "content": content, "parseError": true})
			messages = append(messages,
				map[string]any{"role": "assistant", "content": content},
				map[string]any{"role": "user", "content": "回复必须是严格的 JSON 对象，例如 {\"action\":\"check_env\",\"args\":{}} 或 {\"done\":true,...}。请重新按格式回复。"})
			continue
		}

		if done, _ := action["done"].(bool); done {
			success, _ := action["success"].(bool)
			lastSummary, _ = action["summary"].(string)
			if lastSummary == "" {
				lastSummary = "完成"
			}
			opts.onEvent(map[string]any{"type": "done", "content": lastSummary, "success": success})
			return map[string]any{"ok": success, "summary": lastSummary, "iterations": i + 1}
		}

		tool, _ := action["action"].(string)
		args := map[string]any{}
		if a, ok := action["args"].(map[string]any); ok {
			args = a
		}
		opts.onEvent(map[string]any{"type": "ai", "content": fmt.Sprintf("%s(%s)", tool, truncate(jsonString(args), 200))})

		result := opts.executor(tool, args)
		output := truncate(fmt.Sprint(result["output"]), maxOutput)
		okStr := "失败"
		if ok, _ := result["ok"].(bool); ok {
			okStr = "成功"
		}
		opts.onEvent(map[string]any{"type": "tool", "content": fmt.Sprintf("%s -> %s\n%s", tool, okStr, truncate(output, 400))})

		messages = append(messages,
			map[string]any{"role": "assistant", "content": content},
			map[string]any{"role": "user", "content": fmt.Sprintf(
				"工具 %s 执行%s。输出：\n%s\n\n请根据结果决定下一步。若目标已达成回复 {\"done\":true,...}。",
				tool, okStr, orDefaultStr(output, "（无输出）"))})

		if opts.goalReached != nil && opts.goalReached() {
			opts.onEvent(map[string]any{"type": "goal", "content": "目标检测已达成"})
			return map[string]any{"ok": true, "summary": "目标已达成", "iterations": i + 1}
		}
	}

	return map[string]any{
		"ok":         false,
		"summary":    fmt.Sprintf("达到最大迭代次数 (%d)，智能体停止。%s", maxIterations, lastSummary),
		"iterations": maxIterations,
	}
}

func orEmpty(v any) string {
	if v == nil {
		return "未选择"
	}
	return fmt.Sprint(v)
}

func orDefaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// --- executor: the six tools ------------------------------------------------

var dangerousCommandRe = regexp.MustCompile(`rm\s+-rf?\s+[\/\\](\s|$)|format\s+[a-z]:|del\s+\/s\/q\s+[a-z]:\\|mkfs`)

// executeAgentTool runs one agent tool against the current project.
func executeAgentTool(action string, args map[string]any) map[string]any {
	switch action {
	case "run_command":
		command := strings.TrimSpace(fmt.Sprint(orDefault(args["command"], "")))
		if command == "" {
			return map[string]any{"ok": false, "output": "空命令"}
		}
		if dangerousCommandRe.MatchString(command) {
			return map[string]any{"ok": false, "output": "命令被安全策略拦截（危险操作）"}
		}
		cfgMu.RLock()
		cwd := fmt.Sprint(orDefault(args["cwd"], cfgStr(config, "service.cwd")))
		cfgMu.RUnlock()
		if cwd == "" {
			cwd, _ = os.Getwd()
		}

		var c *exec.Cmd
		if regexp.MustCompile(`[&|><]`).MatchString(command) {
			c = shellCommandRaw(command)
		} else {
			parts := strings.Fields(command)
			program, cmdArgs := parts[0], parts[1:]
			r := resolveWindowsCommand(program, cmdArgs)
			if r.viaShell {
				c = shellCommand(r.program, r.args)
			} else {
				c = exec.Command(r.program, r.args...)
			}
		}
		c.Dir = cwd
		applyHiddenFlags(c)
		var out strings.Builder
		stdout, err := c.StdoutPipe()
		if err != nil {
			return map[string]any{"ok": false, "output": err.Error()}
		}
		stderr, err := c.StderrPipe()
		if err != nil {
			return map[string]any{"ok": false, "output": err.Error()}
		}
		if err := c.Start(); err != nil {
			return map[string]any{"ok": false, "output": err.Error()}
		}
		go func() {
			var dec decodeChunk
			buf := make([]byte, 4096)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					text := dec.decode(buf[:n])
					out.WriteString(text)
					serviceLog.push(text)
				}
				if err != nil {
					break
				}
			}
		}()
		go func() {
			var dec decodeChunk
			buf := make([]byte, 4096)
			for {
				n, err := stderr.Read(buf)
				if n > 0 {
					text := dec.decode(buf[:n])
					out.WriteString(text)
					serviceLog.push(text)
				}
				if err != nil {
					break
				}
			}
		}()
		done := make(chan struct{})
		go func() { _ = c.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(300 * time.Second):
			killProcessTree(c.Process.Pid)
			<-done
			return map[string]any{"ok": false, "output": "命令超时（300 秒）" + truncate(out.String(), 1500)}
		}
		ok := c.ProcessState != nil && c.ProcessState.Success()
		return map[string]any{"ok": ok, "output": truncate(out.String(), 1500)}

	case "write_config":
		cfgMu.RLock()
		path := configPath
		cfgMu.RUnlock()
		if path == "" {
			return map[string]any{"ok": false, "output": "尚未选择项目（无配置文件）"}
		}
		current, err := readRawConfig(path)
		if err != nil {
			return map[string]any{"ok": false, "output": err.Error()}
		}
		patch := map[string]any{}
		if p, ok := args["patch"].(map[string]any); ok {
			patch = p
		}
		merged := deepMerge(current, patch)
		if err := writeRawConfig(path, merged); err != nil {
			return map[string]any{"ok": false, "output": err.Error()}
		}
		cfgMu.Lock()
		config = deepMerge(defaults, merged)
		configPath = path
		cfgMu.Unlock()
		return map[string]any{"ok": true, "output": "配置已更新: " + path}

	case "read_log":
		tail := append(serviceLog.tail(40), buildLog.tail(20)...)
		out := strings.Join(tail, "\n")
		if out == "" {
			out = "（暂无日志）"
		}
		return map[string]any{"ok": true, "output": out}

	case "check_env":
		return map[string]any{"ok": true, "output": jsonString(checkEnvironment())}

	case "build":
		cfgMu.RLock()
		path := configPath
		cfgCopy := config
		cfgMu.RUnlock()
		if path == "" {
			return map[string]any{"ok": false, "output": "尚未选择项目（无配置文件）"}
		}
		dir := filepath.Dir(path)
		res := doBuild(dir, cfgCopy)
		if !res.ok {
			return map[string]any{"ok": false, "output": res.err}
		}
		return map[string]any{"ok": true, "output": "打包成功: " + strings.Join(res.artifacts, ", ")}

	case "run_service":
		cfgMu.RLock()
		cfgCopy := config
		cfgMu.RUnlock()
		stopService()
		if err := startService(cfgCopy); err != nil {
			return map[string]any{"ok": false, "output": "服务启动失败（命令无法执行）: " + err.Error()}
		}
		ensureServiceWindow(cfgCopy)
		return map[string]any{"ok": true, "output": "服务已启动"}

	default:
		return map[string]any{"ok": false, "output": "未知工具: " + action}
	}
}

func shellCommandRaw(command string) *exec.Cmd {
	return exec.Command("cmd.exe", "/d", "/s", "/c", command)
}
