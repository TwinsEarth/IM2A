package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const userIDKey ctxKey = 1

// WithUserID 把已认证用户 id 注入请求上下文。
func WithUserID(r *http.Request, uid int64) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userIDKey, uid))
}

// UserIDFromContext 取出中间件注入的用户 id；第二个返回值表示是否存在。
func UserIDFromContext(r *http.Request) (int64, bool) {
	uid, ok := r.Context().Value(userIDKey).(int64)
	return uid, ok
}

// bearerToken 从 Authorization 头或 access_token query（WS/SSE 用）取出 token。
func bearerToken(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	if q := r.URL.Query().Get("access_token"); q != "" {
		return q
	}
	return ""
}

// RequireIdentity 校验 JWT，把 sub 作为 user_id 注入 context。
// 任何缺失/篡改/过期都 401，绝不放行匿名请求。
func RequireIdentity(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			http.Error(w, `{"error":"unauthorized","reason":"missing token"}`, http.StatusUnauthorized)
			return
		}
		c, err := Parse(secret, tok)
		if err != nil {
			http.Error(w, `{"error":"unauthorized","reason":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, WithUserID(r, c.Sub))
	})
}


// Authenticate 统一认证中间件（v1.9.1 横向越权收紧）。
//   - 静态共享 token（与静态 token 相等）：放行，但不注入身份（向后兼容 demo/服务间调用）；
//   - 合法 JWT：放行并把 sub 注入 request context（业务路由据此确定调用者）；
//   - 空 token：仅当 staticToken 为开发空值时放行；否则 401；
//   - 其它一律 401。
func Authenticate(next http.Handler, staticToken, jwtSecret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			if staticToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if staticToken != "" && tok == staticToken {
			next.ServeHTTP(w, r)
			return
		}
		if jwtSecret != "" {
			if c, err := Parse(jwtSecret, tok); err == nil && c.Sub > 0 {
				next.ServeHTTP(w, WithUserID(r, c.Sub))
				return
			}
		}
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
}
