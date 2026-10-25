// Package auth 提供 HTTP Bearer Token 认证中间件。
//
// v0.5.1 最小实现：静态 token 校验（生产切换到 PostgreSQL + etcd）。
// 用法：
//
//	mux.Handle("/v1/", auth.RequireToken(handler, "secret-token"))
package auth

import (
	"net/http"
	"strings"
)

// RequireToken 校验 Authorization: Bearer <token>。
// token 为空时跳过校验（开发模式）。
func RequireToken(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimPrefix(auth, "Bearer ")
		} else if q := r.URL.Query().Get("access_token"); q != "" {
			// WebSocket / EventSource 无法设置 Authorization 头，允许 query 传 token。
			got = q
		}
		if got != token {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
