package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger 创建结构化 JSON 日志器。
//
// 级别由环境变量 LOG_LEVEL 决定，默认 info。此前级别是硬编码的，
// 服务没有任何临时开启 debug 的手段，诊断只能靠改代码重新部署。
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(os.Getenv("LOG_LEVEL"))}))
}

// parseLogLevel 解析日志级别名，无法识别的值回退到 info，
// 避免一个拼错的环境变量把日志打没了。
func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
