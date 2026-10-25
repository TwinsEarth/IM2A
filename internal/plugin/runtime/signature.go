// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Ed25519 manifest 签名校验。
//
// 签名对象是 **canonical JSON 的 sha256 摘要**（不是原始文件字节），因此同一 manifest
// 的键序/空白不同不影响验签；签名文件为 detached 二进制 `manifest.sig`（64 字节），
// 公钥 keyId 取自 manifest.publisher.keyId，公钥集合由部署方配置（TrustStore）。
//
// 四态必须给具名错误（不允许静默降级）：
//   - 有效        → nil
//   - 缺失        → ErrSignatureMissing
//   - 未知 key    → ErrUnknownPublisherKey
//   - 不匹配/篡改 → ErrBadSignature
// 另有格式错误（长度非 64）→ ErrSignatureMalformed；信任库不可用 → ErrTrustStoreUnavailable。

// 签名/信任库错误（具名，可用 errors.Is 判定）。
var (
	ErrSignatureMissing      = errors.New("plugin runtime: manifest signature missing")
	ErrSignatureMalformed    = errors.New("plugin runtime: manifest signature malformed")
	ErrUnknownPublisherKey   = errors.New("plugin runtime: unknown publisher key")
	ErrBadSignature          = errors.New("plugin runtime: manifest signature does not match content")
	ErrTrustStoreUnavailable = errors.New("plugin runtime: trust store unavailable")
)

// SignatureStatus 是给 JSON/CLI 输出的签名状态字符串（与错误一一对应，不吞错）。
type SignatureStatus string

const (
	SigValid       SignatureStatus = "valid"
	SigMissing     SignatureStatus = "missing"
	SigMalformed   SignatureStatus = "malformed"
	SigUnknownKey  SignatureStatus = "unknown_key"
	SigBad         SignatureStatus = "bad"
	SigNotRequired SignatureStatus = "not_required"
)

// TrustStore 是 keyId → Ed25519 公钥 的只读集合。
type TrustStore struct {
	keys map[string]ed25519.PublicKey
}

// NewTrustStore 创建空信任库。
func NewTrustStore() *TrustStore { return &TrustStore{keys: map[string]ed25519.PublicKey{}} }

// Add 添加一个受信公钥；pub 长度必须为 ed25519.PublicKeySize。
func (t *TrustStore) Add(keyID string, pub ed25519.PublicKey) error {
	if t == nil {
		return ErrTrustStoreUnavailable
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return fmt.Errorf("%w: empty key id", ErrUnknownPublisherKey)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: key %q has %d bytes, want %d", ErrUnknownPublisherKey, keyID, len(pub), ed25519.PublicKeySize)
	}
	if t.keys == nil {
		t.keys = map[string]ed25519.PublicKey{}
	}
	t.keys[keyID] = append(ed25519.PublicKey(nil), pub...)
	return nil
}

// Lookup 查找公钥。
func (t *TrustStore) Lookup(keyID string) (ed25519.PublicKey, bool) {
	if t == nil {
		return nil, false
	}
	pub, ok := t.keys[keyID]
	return pub, ok
}

// Len 返回受信 key 数量。
func (t *TrustStore) Len() int {
	if t == nil {
		return 0
	}
	return len(t.keys)
}

// LoadTrustDir 从目录加载受信公钥：每个 `*.pub` 文件是 PKIX PEM 公钥，文件名（去 .pub）即 keyId。
// 目录不存在/为空返回 ErrTrustStoreUnavailable（K1/K2 没有信任库就不能验签，必须显式失败）。
func LoadTrustDir(dir string) (*TrustStore, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTrustStoreUnavailable, err)
	}
	ts := NewTrustStore()
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", ErrTrustStoreUnavailable, path, err)
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("%w: %s is not a PEM PUBLIC KEY", ErrTrustStoreUnavailable, path)
		}
		pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: parse %s: %v", ErrTrustStoreUnavailable, path, err)
		}
		pub, ok := pubAny.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: %s is not an Ed25519 key", ErrTrustStoreUnavailable, path)
		}
		keyID := strings.TrimSuffix(e.Name(), ".pub")
		if err := ts.Add(keyID, pub); err != nil {
			return nil, err
		}
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: no *.pub keys in %s", ErrTrustStoreUnavailable, dir)
	}
	return ts, nil
}

// SignatureState 把验签错误映射为状态字符串（nil → valid；未知错误 → bad，不吞错）。
func SignatureState(err error) SignatureStatus {
	switch {
	case err == nil:
		return SigValid
	case errors.Is(err, ErrSignatureMissing):
		return SigMissing
	case errors.Is(err, ErrSignatureMalformed):
		return SigMalformed
	case errors.Is(err, ErrUnknownPublisherKey), errors.Is(err, ErrTrustStoreUnavailable):
		return SigUnknownKey
	default:
		return SigBad
	}
}

// Verify 校验 manifest 的 detached 签名。store 为 nil 时返回 ErrTrustStoreUnavailable
// （无信任库时不可能确认来源，必须失败而不是放行）。
func Verify(rawManifest, sig []byte, store *TrustStore) error {
	if len(sig) == 0 {
		return ErrSignatureMissing
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrSignatureMalformed, len(sig), ed25519.SignatureSize)
	}
	if store == nil || store.Len() == 0 {
		return fmt.Errorf("%w: no trusted keys configured", ErrTrustStoreUnavailable)
	}
	m, _, err := Parse(rawManifest)
	if err != nil {
		return fmt.Errorf("%w: manifest invalid before verify: %v", ErrBadSignature, err)
	}
	pub, ok := store.Lookup(m.Publisher.KeyID)
	if !ok {
		return fmt.Errorf("%w: keyId %q not in trust store", ErrUnknownPublisherKey, m.Publisher.KeyID)
	}
	canonical, err := Canonicalize(rawManifest)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	if !ed25519.Verify(pub, canonical, sig) {
		return fmt.Errorf("%w: keyId %q", ErrBadSignature, m.Publisher.KeyID)
	}
	return nil
}

// SignManifest 用私钥对 canonical JSON 签名（测试/安装工具共用；生产只验签不持私钥）。
func SignManifest(priv ed25519.PrivateKey, rawManifest []byte) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("plugin runtime: bad ed25519 private key length %d", len(priv))
	}
	canonical, err := Canonicalize(rawManifest)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, canonical), nil
}
