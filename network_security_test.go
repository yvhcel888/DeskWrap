package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func setNetworkSecurityTestConfig(t *testing.T, cfg map[string]any) {
	t.Helper()
	cfgMu.Lock()
	previous := config
	config = cfg
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		config = previous
		cfgMu.Unlock()
	})
}

func TestAISettingsFromOptsIsolatesProviderKeys(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{
		"ai": map[string]any{
			"enabled":  true,
			"provider": "openai",
			"apiKey":   "stored-openai-key",
			"baseUrl":  "https://stored.invalid/v1/chat/completions",
			"model":    "stored-model",
		},
	})

	tests := []struct {
		name     string
		opts     map[string]any
		envKey   string
		wantKey  string
		wantBase string
	}{
		{
			name:     "same explicit provider inherits stored key",
			opts:     map[string]any{"provider": "openai"},
			wantKey:  "stored-openai-key",
			wantBase: "https://stored.invalid/v1/chat/completions",
		},
		{
			name:     "omitted provider inherits stored key",
			opts:     map[string]any{},
			wantKey:  "stored-openai-key",
			wantBase: "https://stored.invalid/v1/chat/completions",
		},
		{
			name:     "empty provider counts as omitted",
			opts:     map[string]any{"provider": ""},
			wantKey:  "stored-openai-key",
			wantBase: "https://stored.invalid/v1/chat/completions",
		},
		{
			name:     "changed provider uses its environment key",
			opts:     map[string]any{"provider": "deepseek"},
			envKey:   "selected-deepseek-key",
			wantKey:  "selected-deepseek-key",
			wantBase: aiProviders["deepseek"].base,
		},
		{
			name:     "changed provider without environment key has no key",
			opts:     map[string]any{"provider": "deepseek"},
			wantKey:  "",
			wantBase: aiProviders["deepseek"].base,
		},
		{
			name:     "explicit key wins for changed provider",
			opts:     map[string]any{"provider": "deepseek", "apiKey": "explicit-key"},
			envKey:   "selected-deepseek-key",
			wantKey:  "explicit-key",
			wantBase: aiProviders["deepseek"].base,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DEEPSEEK_API_KEY", tt.envKey)
			got := aiSettingsFromOpts(tt.opts)
			if got["apiKey"] != tt.wantKey {
				t.Fatalf("apiKey = %q, want %q", got["apiKey"], tt.wantKey)
			}
			if got["base"] != tt.wantBase {
				t.Fatalf("base = %q, want %q", got["base"], tt.wantBase)
			}
		})
	}
}

func TestAITestDoesNotReuseStoredKeyAfterProviderChange(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{
		"ai": map[string]any{"provider": "openai", "apiKey": "stored-openai-key"},
	})
	t.Setenv("DEEPSEEK_API_KEY", "")

	var requests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer server.Close()

	got := aiTest(map[string]any{
		"provider": "deepseek",
		"baseUrl":  server.URL + "/v1/chat/completions",
		"model":    "test-model",
	})
	if got["ok"] == true {
		t.Fatalf("aiTest unexpectedly accepted another provider's stored key: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("aiTest made %d requests without the selected provider's key", requests)
	}
}

func TestAIModelsUsesSelectedProviderEnvironmentKey(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{
		"ai": map[string]any{"provider": "openai", "apiKey": "stored-openai-key"},
	})
	t.Setenv("DEEPSEEK_API_KEY", "selected-deepseek-key")

	var mu sync.Mutex
	var authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model-b"},{"id":"model-a"}]}`)
	}))
	defer server.Close()

	got := aiModels(map[string]any{
		"provider": "deepseek",
		"baseUrl":  server.URL + "/v1/chat/completions",
	})
	if got["ok"] != true {
		t.Fatalf("aiModels failed: %v", got)
	}
	models, ok := got["models"].([]string)
	if !ok || len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("provider model search changed: %v", got["models"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authorizations) == 0 {
		t.Fatal("aiModels made no requests")
	}
	for _, authorization := range authorizations {
		if authorization != "Bearer selected-deepseek-key" {
			t.Fatalf("Authorization = %q, want selected provider key only", authorization)
		}
	}
}

func TestAppFetchResponseSizeLimit(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{"proxy": map[string]any{"enabled": false}})
	const limit = 4 << 20
	for _, size := range []int{limit, limit + 1} {
		t.Run(fmt.Sprintf("%d_bytes", size), func(t *testing.T) {
			body := make([]byte, size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(body)
			}))
			defer server.Close()

			data, status, err := appFetch("GET", server.URL, nil, nil, 5*time.Second)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if size == limit {
				if err != nil || len(data) != size {
					t.Fatalf("exact-limit response: len=%d err=%v", len(data), err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
				t.Fatalf("overflow error = %v, want explicit size-limit error", err)
			}
			if data != nil {
				t.Fatalf("overflow returned %d bytes instead of nil", len(data))
			}
		})
	}
}

func TestAppFetchRedactsSensitiveQueryValuesInErrors(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{"proxy": map[string]any{"enabled": false}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	secrets := []string{"query-access-secret", "query-token-secret", "query-api-secret"}
	url := server.URL + "/?access_token=" + secrets[0] + "&token=" + secrets[1] + "&api_key=" + secrets[2] + "&q=visible"
	_, _, err := appFetch("GET", url, nil, nil, 5*time.Second)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "q=visible") {
		t.Fatalf("redaction removed non-sensitive query data: %v", err)
	}
}

func TestAppFetchStripsCredentialsOnCrossOriginRedirect(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{"proxy": map[string]any{"enabled": false}})
	var destinationChecked bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationChecked = true
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("cross-origin Authorization = %q", got)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "" {
			t.Errorf("cross-origin PRIVATE-TOKEN = %q", got)
		}
		if got := r.Header.Get("X-Custom-Credential"); got != "" {
			t.Errorf("cross-origin custom credential = %q", got)
		}
		query := r.URL.Query()
		for _, key := range []string{"access_token", "token", "api_key"} {
			if _, ok := query[key]; ok {
				t.Errorf("cross-origin query retained %s", key)
			}
		}
		if query.Get("q") != "kept" {
			t.Errorf("cross-origin non-sensitive query = %q", query.Get("q"))
		}
		fmt.Fprint(w, "redirected")
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer origin-secret" ||
			r.Header.Get("PRIVATE-TOKEN") != "private-secret" ||
			r.Header.Get("X-Custom-Credential") != "custom-secret" ||
			r.URL.Query().Get("access_token") != "origin-query-secret" {
			t.Error("origin did not receive the test credentials")
		}
		http.Redirect(w, r, destination.URL+"/next?access_token=redirect-secret&token=redirect-token&api_key=redirect-key&q=kept", http.StatusFound)
	}))
	defer origin.Close()

	headers := map[string]string{
		"Authorization":       "Bearer origin-secret",
		"PRIVATE-TOKEN":       "private-secret",
		"X-Custom-Credential": "custom-secret",
		"Accept":              "text/plain",
	}
	data, status, err := appFetch("GET", origin.URL+"/?access_token=origin-query-secret", headers, nil, 5*time.Second)
	if err != nil || status != http.StatusOK || string(data) != "redirected" {
		t.Fatalf("redirect fetch: status=%d data=%q err=%v", status, data, err)
	}
	if !destinationChecked {
		t.Fatal("redirect destination was not checked")
	}
}

func TestAppFetchPreservesCredentialsOnSameOriginRedirect(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{"proxy": map[string]any{"enabled": false}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer same-origin-secret" {
			t.Errorf("same-origin Authorization = %q", got)
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	data, status, err := appFetch("GET", server.URL+"/start", map[string]string{"Authorization": "Bearer same-origin-secret"}, nil, 5*time.Second)
	if err != nil || status != http.StatusOK || string(data) != "ok" {
		t.Fatalf("same-origin redirect: status=%d data=%q err=%v", status, data, err)
	}
}

func TestAppFetchRejectsCrossOriginPostRedirect(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{"proxy": map[string]any{"enabled": false}})
	var destinationCalled bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalled = true
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	_, _, err := appFetch("POST", origin.URL, map[string]string{"Authorization": "Bearer secret"}, []byte("private logs"), 5*time.Second)
	if err == nil {
		t.Fatal("cross-origin POST redirect was accepted")
	}
	if destinationCalled {
		t.Fatal("private POST body was replayed to a different origin")
	}
}

func TestPlatformSearchUsesAuthenticationHeaders(t *testing.T) {
	setNetworkSecurityTestConfig(t, map[string]any{})
	previous := defaultPlatforms
	t.Cleanup(func() { defaultPlatforms = previous })

	tests := []struct {
		id         string
		authHeader string
		wantHeader string
		wantValue  string
	}{
		{id: "github", wantHeader: "Authorization", wantValue: "Bearer platform-secret"},
		{id: "gitee", wantHeader: "Authorization", wantValue: "Bearer platform-secret"},
		{id: "gitlab", wantHeader: "PRIVATE-TOKEN", wantValue: "platform-secret"},
		{id: "modelscope", wantHeader: "Authorization", wantValue: "Bearer platform-secret"},
		{id: "codeberg", wantHeader: "Authorization", wantValue: "token platform-secret"},
		{id: "huggingface", wantHeader: "Authorization", wantValue: "Bearer platform-secret"},
		{id: "custom", authHeader: "X-API-Key", wantHeader: "X-API-Key", wantValue: "Bearer platform-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(tt.wantHeader); got != tt.wantValue {
					t.Errorf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
				}
				if _, ok := r.URL.Query()["access_token"]; ok {
					t.Error("platform token leaked into the query string")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"items":[]}`)
			}))
			defer server.Close()

			platform := map[string]any{
				"id":       tt.id,
				"search":   server.URL + "?q={q}",
				"listPath": "items",
				"token":    "platform-secret",
			}
			if tt.authHeader != "" {
				platform["authHeader"] = tt.authHeader
			}
			defaultPlatforms = []map[string]any{platform}
			got := platformSearch(tt.id, "test query")
			if got["ok"] != true {
				t.Fatalf("platformSearch failed: %v", got)
			}
		})
	}
}

func TestResolveProjectInputRejectsUnsafeCloneNames(t *testing.T) {
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	setNetworkSecurityTestConfig(t, map[string]any{"projectsDir": projectsRoot})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unsafe clone input unexpectedly reached the HTTP server")
	}))
	defer server.Close()

	for _, suffix := range []string{"/..", "/%2e%2e", "/..%5Coutside", `/..\outside`} {
		t.Run(fmt.Sprintf("suffix_%q", suffix), func(t *testing.T) {
			// Make the legacy destination exist so the vulnerable implementation
			// returns without invoking git or making a network request.
			name := strings.TrimSuffix(suffix, ".git")
			if i := strings.LastIndexAny(name, "/:"); i >= 0 {
				name = name[i+1:]
			}
			legacyTarget := filepath.Join(projectsRoot, name)
			if err := os.MkdirAll(legacyTarget, 0o755); err != nil {
				t.Fatalf("prepare legacy destination: %v", err)
			}

			got := resolveProjectInput(server.URL + suffix)
			if got["ok"] == true {
				t.Fatalf("unsafe repository name was accepted: %v", got)
			}
			if got["error"] == "" || got["error"] == nil {
				t.Fatalf("missing validation error: %v", got)
			}
		})
	}
}
