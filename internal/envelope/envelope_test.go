package envelope

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundtrip(t *testing.T) {
	key := testKey(t)
	pt := []byte("用户的银行账户 6222 **** 私密 Context")
	nonce, ct, err := Encrypt(key, pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, pt) {
		t.Fatal("密文不应包含明文片段")
	}
	got, err := Decrypt(key, nonce, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestWrongKeyFails(t *testing.T) {
	k1 := testKey(t)
	k2 := testKey(t)
	nonce, ct, _ := Encrypt(k1, []byte("secret"))
	if _, err := Decrypt(k2, nonce, ct); err == nil {
		t.Fatal("错密钥必须解密失败")
	}
}

func TestTamperFails(t *testing.T) {
	key := testKey(t)
	nonce, ct, _ := Encrypt(key, []byte("secret"))
	ct[len(ct)-1] ^= 0xFF // 篡改认证 tag
	if _, err := Decrypt(key, nonce, ct); err == nil {
		t.Fatal("篡改密文必须解密失败")
	}
}

func TestShortKeyRejected(t *testing.T) {
	if _, _, err := Encrypt(make([]byte, 16), []byte("x")); err != ErrInvalidKey {
		t.Fatal("16 字节密钥应被拒绝")
	}
	if _, err := Decrypt(make([]byte, 16), make([]byte, 12), []byte("x")); err != ErrInvalidKey {
		t.Fatal("16 字节密钥应被拒绝")
	}
}
