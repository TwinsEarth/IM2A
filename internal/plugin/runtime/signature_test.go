// SPDX-License-Identifier: MIT

package pluginruntime

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 签名四态（有效/无效/未知 key/缺失）+ 格式错误 + 无信任库，全部具名错误。
func TestSignatureFourStates(t *testing.T) {
	pub, priv := testKeyPair(t)
	m := baseManifestMap("sig-test", "1.0.0", KindK1)
	raw := mustJSON(t, m)

	sig, err := SignManifest(priv, raw)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	ts := NewTrustStore()
	if err := ts.Add(testKeyID, pub); err != nil {
		t.Fatal(err)
	}

	// 1) 有效。
	if err := Verify(raw, sig, ts); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	// 2) 无效（签名被篡改）。
	bad := append([]byte(nil), sig...)
	bad[0] ^= 0xFF
	if err := Verify(raw, bad, ts); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered signature: want ErrBadSignature, got %v", err)
	}

	// 3) 未知 key（信任库里没有该 keyId）。
	other := NewTrustStore()
	otherPub, _ := testKeyPairDeterministic(t, "another-key")
	if err := other.Add("another-key-01", otherPub); err != nil {
		t.Fatal(err)
	}
	if err := Verify(raw, sig, other); !errors.Is(err, ErrUnknownPublisherKey) {
		t.Fatalf("unknown key: want ErrUnknownPublisherKey, got %v", err)
	}

	// 4) 缺失。
	if err := Verify(raw, nil, ts); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("missing signature: want ErrSignatureMissing, got %v", err)
	}

	// 附加：格式错误（长度非 64）。
	if err := Verify(raw, sig[:ed25519.SignatureSize-1], ts); !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("malformed signature: want ErrSignatureMalformed, got %v", err)
	}

	// 附加：没有信任库不能放行。
	if err := Verify(raw, sig, nil); !errors.Is(err, ErrTrustStoreUnavailable) {
		t.Fatalf("nil trust store: want ErrTrustStoreUnavailable, got %v", err)
	}
}

// 签名对象是 canonical JSON：manifest 键序/空白不同不影响验签，但内容变化必须失败。
func TestSignManifestCanonicalAndContentBinding(t *testing.T) {
	pub, priv := testKeyPair(t)
	canonical := mustJSON(t, baseManifestMap("sig-binding", "1.0.0", KindK1))
	// 另一份等价写法（键序变化）：先解析成 map 再 marshal 顺序仍稳定，用缩进+字段重排模拟。
	rawA := canonical
	rawB := []byte("{\n  \"kind\": \"K1\",\n  \"id\": \"sig-binding\",\n  \"name\": \"Test sig-binding\",\n  \"version\": \"1.0.0\",\n  \"apiVersion\": \"im2a.plugin/v1\",\n  \"api\": \"im2a.capability/v1\",\n  \"publisher\": {\"keyId\": \"test-key-01\", \"id\": \"im2a-test\"},\n  \"entrypoint\": {\"args\": [\"--serve\"], \"path\": \"bin/sig-binding\", \"type\": \"exec\"},\n  \"provides\": [{\"version\": \"v1\", \"id\": \"test.sig-binding.cap\"}],\n  \"permissions\": [],\n  \"attribution\": {\"license\": \"MIT\", \"copyright\": \"Copyright (c) 2026 Test\", \"upstream\": \"https://example.com/upstream\", \"derived\": false, \"usage\": [\"test fixture\"]}\n}\n")

	ts := NewTrustStore()
	if err := ts.Add(testKeyID, pub); err != nil {
		t.Fatal(err)
	}
	sig, err := SignManifest(priv, rawA)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(rawB, sig, ts); err != nil {
		t.Fatalf("equivalent JSON rejected: %v", err)
	}

	// 内容变化（version 1.0.0 → 1.0.1）→ 旧签名必须失败。
	tampered := baseManifestMap("sig-binding", "1.0.1", KindK1)
	if err := Verify(mustJSON(t, tampered), sig, ts); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("content change with old signature: want ErrBadSignature, got %v", err)
	}
}

// SignatureState 映射（JSON/CLI 输出用，不允许吞错）。
func TestSignatureStateMapping(t *testing.T) {
	cases := []struct {
		err  error
		want SignatureStatus
	}{
		{nil, SigValid},
		{ErrSignatureMissing, SigMissing},
		{ErrSignatureMalformed, SigMalformed},
		{ErrUnknownPublisherKey, SigUnknownKey},
		{ErrTrustStoreUnavailable, SigUnknownKey},
		{ErrBadSignature, SigBad},
	}
	for _, c := range cases {
		if got := SignatureState(c.err); got != c.want {
			t.Errorf("SignatureState(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}

// 信任库目录加载：PKIX PEM，文件名=keyId；空目录必须失败（不能静默无 key）。
func TestLoadTrustDir(t *testing.T) {
	pub, priv := testKeyPair(t)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, testKeyID+".pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, err := LoadTrustDir(dir)
	if err != nil {
		t.Fatalf("LoadTrustDir: %v", err)
	}
	if _, ok := ts.Lookup(testKeyID); !ok {
		t.Fatalf("key %s not loaded", testKeyID)
	}
	// 用加载出的信任库验签。
	raw := mustJSON(t, baseManifestMap("trust-dir", "1.0.0", KindK1))
	sig, err := SignManifest(priv, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(raw, sig, ts); err != nil {
		t.Fatalf("verify via loaded trust dir: %v", err)
	}

	empty := t.TempDir()
	if _, err := LoadTrustDir(empty); !errors.Is(err, ErrTrustStoreUnavailable) {
		t.Fatalf("empty trust dir: want ErrTrustStoreUnavailable, got %v", err)
	}
}

// testKeyPairDeterministic 从固定字符串派生第二对测试密钥。
func testKeyPairDeterministic(t *testing.T, seedStr string) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, []byte(seedStr))
	priv := ed25519.NewKeyFromSeed(seed)
	pub, _ := priv.Public().(ed25519.PublicKey)
	return pub, priv
}
