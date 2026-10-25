// Package envelope 提供 AES-256-GCM 信封加密（v1.9.2 端到端加密）。
// 设计原则：服务端只落密文与 nonce，明文密钥由客户端持有并在请求头传入；
// 服务端不持久化任何密钥，因此 DBA / 被拖库也无法还原明文。
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// ErrInvalidKey 密钥长度不是 AES-256 所需的 32 字节。
var ErrInvalidKey = errors.New("envelope: key must be 32 bytes (AES-256)")

// Encrypt 用 32 字节密钥加密明文，返回随机 nonce 与 GCM 密文（含认证 tag）。
// nonce 与 ciphertext 均可公开存储；缺密钥者无法解密。
func Encrypt(key, plaintext []byte) (nonce, ciphertext []byte, err error) {
	if len(key) != 32 {
		return nil, nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	// Seal 把 nonce 前缀留空，密文末尾附 GCM 认证 tag
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return nonce, ciphertext, nil
}

// Decrypt 校验并解密密文；密钥错误或密文被篡改都会返回非 nil error（GCM 认证）。
func Decrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("envelope: invalid nonce size")
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}
