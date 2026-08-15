// DeskWrap - locale resolution and main-process log localization.
// DeskWrap's own log lines follow the UI language; third-party output is
// left as-is (it carries the program's own language).
package main

import "strings"

var l10n = map[string]map[string]string{
	"zh-CN": {
		"buildLogTail":     "构建日志尾部",
		"cloneFailed":      "克隆失败",
		"startService":     "正在启动服务",
		"workDir":          "工作目录",
		"unknownAddr":      "无法识别地址",
		"emptyAddr":        "地址为空",
		"configLoadFailed": "读取配置失败",
		"serviceExited":    "服务已退出",
		"stoppingService":  "正在停止服务",
		"autoRestarting":   "服务异常退出，1.5 秒后自动重启",
		"trayShow":         "显示窗口",
		"trayQuit":         "退出",
		"envCheckTitle":    "环境检查",
	},
	"en-US": {
		"buildLogTail":     "build log tail",
		"cloneFailed":      "Clone failed",
		"startService":     "Starting service",
		"workDir":          "Working directory",
		"unknownAddr":      "Unrecognized address",
		"emptyAddr":        "Address is empty",
		"configLoadFailed": "Failed to read config",
		"serviceExited":    "Service exited",
		"stoppingService":  "Stopping service",
		"autoRestarting":   "Service exited unexpectedly, restarting in 1.5s",
		"trayShow":         "Show window",
		"trayQuit":         "Quit",
		"envCheckTitle":    "Environment check",
	},
}

// resolveLocale returns the effective UI locale: an explicit config value
// wins, otherwise it follows the system UI language (zh* → zh-CN).
func resolveLocale() string {
	cfgMu.RLock()
	l := cfgStr(config, "locale")
	cfgMu.RUnlock()
	if l == "zh-CN" || l == "en-US" {
		return l
	}
	if strings.HasPrefix(strings.ToLower(systemLocale()), "zh") {
		return "zh-CN"
	}
	return "en-US"
}

func t(key string) string {
	dict := l10n[resolveLocale()]
	if dict == nil {
		dict = l10n["zh-CN"]
	}
	if s, ok := dict[key]; ok {
		return s
	}
	return key
}
