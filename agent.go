// DeskWrap - AI diagnosis and provider plumbing.
//
// The AI diagnosis reads the service log, identifies the root cause, and
// suggests fixes.  All AI traffic goes through appFetch (proxy-aware):
// the packaged app must respect the user's optional proxy, and Go's
// default HTTP client does not automatically pick up a GUI-set proxy URL.
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
	"regexp"
	"strings"
	"time"
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
// connection test without saving, so diagnosis must honor the form just
// like the test does.
func aiSettingsFromOpts(opts map[string]any) map[string]any {
	cfg := getAiConfig()
	if opts == nil {
		return cfg
	}
	provider := fmt.Sprint(orDefault(opts["provider"], fmt.Sprint(cfg["provider"])))
	prov := providerOr(provider)
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
		headers := map[string]string{}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
		data, status, err := appFetch("GET", u, headers, nil, 15*time.Second)
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
