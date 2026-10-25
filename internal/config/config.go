// Package config 提供服务通用配置加载（flag + 环境变量覆盖）。
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
)

// Config 服务通用配置。
type Config struct {
	ServiceName string
	Port        int    // healthz 端口
	Env         string // dev / staging / prod
	LogDir      string // 审计日志目录（空则输出到 stderr）
}

// Load 解析 flag 与环境变量。flag 优先于环境变量，环境变量优先于默认值。
func Load(serviceName string) *Config {
	port := flag.Int("port", 8080, "healthz HTTP port")
	env := flag.String("env", "dev", "runtime env: dev/staging/prod")
	logDir := flag.String("log-dir", "", "audit log directory (empty = stderr)")
	flag.Parse()

	cfg := &Config{
		ServiceName: serviceName,
		Port:        *port,
		Env:         *env,
		LogDir:      *logDir,
	}
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("APP_ENV"); v != "" {
		cfg.Env = v
	}
	if v := os.Getenv("AUDIT_LOG_DIR"); v != "" {
		cfg.LogDir = v
	}
	return cfg
}

// Addr 返回 healthz 监听地址。
func (c *Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
