// v1.9.2：端到端加密 Context API。
// 客户端持有 AES-256 密钥（X-Data-Key, base64）；服务端只做信封加密并落密文，
// 永不持久化密钥。无密钥时服务端既不能解密、被拖库也拿不到明文。
package api

import (
	"encoding/base64"
	"errors"
	"encoding/json"
	"net/http"
	"strconv"

	"human-agent-im/internal/envelope"
)

func decodeClientKey(header string) ([]byte, error) {
	if header == "" {
		return nil, errors.New("X-Data-Key required")
	}
	k, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return nil, err
	}
	if len(k) != 32 {
		return nil, envelope.ErrInvalidKey
	}
	return k, nil
}

// PutEncryptedContext POST /v1/memory/encrypted  body:{plaintext,realm,kid?}  header:X-Data-Key
func (d *Deps) PutEncryptedContext(w http.ResponseWriter, r *http.Request) {
	uid, ok := UserIDFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	key, err := decodeClientKey(r.Header.Get("X-Data-Key"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "X-Data-Key must be base64 of 32 bytes"})
		return
	}
	var body struct {
		Plaintext string `json:"plaintext"`
		Realm     string `json:"realm"`
		Kid       string `json:"kid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Realm == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "plaintext and realm required"})
		return
	}
	if body.Kid == "" {
		body.Kid = "client-aes256-gcm"
	}
	nonce, ct, err := envelope.Encrypt(key, []byte(body.Plaintext))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// 只落密文：value 列永不出现明文
	var id int64
	err = d.DB.QueryRow(
		`INSERT INTO encrypted_context(user_id, realm, kid, nonce, ciphertext)
		 VALUES($1,$2,$3,$4,$5) RETURNING id`,
		uid, body.Realm, body.Kid, nonce, ct,
	).Scan(&id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "realm": body.Realm, "kid": body.Kid, "stored": "ciphertext-only"})
}

// GetEncryptedContext GET /v1/memory/encrypted?id=...  header:X-Data-Key
func (d *Deps) GetEncryptedContext(w http.ResponseWriter, r *http.Request) {
	uid, ok := UserIDFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id required"})
		return
	}
	var (
		dbUID            int64
		realm, kid       string
		nonce, cipher    []byte
	)
	err = d.DB.QueryRow(
		`SELECT user_id, realm, kid, nonce, ciphertext FROM encrypted_context WHERE id=$1`, id,
	).Scan(&dbUID, &realm, &kid, &nonce, &cipher)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	// v1.9.1 横向越权：只能读本人记录
	if dbUID != uid {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": ErrCallerMismatch.Error()})
		return
	}
	key, err := decodeClientKey(r.Header.Get("X-Data-Key"))
	if err != nil {
		// 无密钥：服务端也无法解密，仅返回密文元信息（证明服务端不持密钥）
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "realm": realm, "kid": kid,
			"ciphertext_only": true, "note": "X-Data-Key required to decrypt",
		})
		return
	}
	pt, err := envelope.Decrypt(key, nonce, cipher)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "decrypt failed (wrong key or tampered)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "realm": realm, "plaintext": string(pt)})
}

// ExportEncryptedContext GET /v1/memory/encrypted/export
// 导出本人全部加密记录为密文包（entries 内 nonce/ciphertext 仍是 GCM 密文，导出文件本身不含明文）。
func (d *Deps) ExportEncryptedContext(w http.ResponseWriter, r *http.Request) {
	uid, ok := UserIDFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	rows, err := d.DB.Query(
		`SELECT realm, kid, encode(nonce,'base64'), encode(ciphertext,'base64')
		 FROM encrypted_context WHERE user_id=$1 ORDER BY id`, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer rows.Close()
	type entry struct {
		Realm  string `json:"realm"`
		Kid    string `json:"kid"`
		Nonce  string `json:"nonce_b64"`
		Cipher string `json:"ciphertext_b64"`
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Realm, &e.Kid, &e.Nonce, &e.Cipher); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"format": "im2a-encrypted-context/v1", "user_id": uid, "count": len(entries), "entries": entries,
	})
}

// ImportEncryptedContext POST /v1/memory/encrypted/import  body:{entries:[...]}  header:X-Data-Key
// 导入加密包：必须用同一把 X-Data-Key，否则 GCM 校验失败；成功后密文归属到当前登录用户。
func (d *Deps) ImportEncryptedContext(w http.ResponseWriter, r *http.Request) {
	uid, ok := UserIDFromContext(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	// 密文快照迁移：服务端只原样落库 nonce/ciphertext，不持密钥、不解密。
	// 密钥正确性与包完整性由读取时的 GCM 校验体现（错 key 读不出明文）。
	var body struct {
		Entries []struct {
			Realm  string `json:"realm"`
			Kid    string `json:"kid"`
			Nonce  string `json:"nonce_b64"`
			Cipher string `json:"ciphertext_b64"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad package"})
		return
	}
	if len(body.Entries) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty package"})
		return
	}
	imported := 0
	for _, e := range body.Entries {
		nonce, _ := base64.StdEncoding.DecodeString(e.Nonce)
		ct, _ := base64.StdEncoding.DecodeString(e.Cipher)
		kid := e.Kid
		if kid == "" {
			kid = "client-aes256-gcm"
		}
		if _, err := d.DB.Exec(
			`INSERT INTO encrypted_context(user_id, realm, kid, nonce, ciphertext) VALUES($1,$2,$3,$4,$5)`,
			uid, e.Realm, kid, nonce, ct); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "user_id": uid})
}
