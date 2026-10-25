// SPDX-License-Identifier: MIT
package connapi

import "testing"

func TestTopicUserID(t *testing.T) {
	cases := []struct {
		topic string
		want  int64
		ok    bool
	}{
		{"approval:10086", 10086, true},
		{"trust:42", 42, true},
		{"user:7", 7, true},
		{" approval:9 ", 9, true},
		{"approval:0", 0, false},
		{"approval:-5", 0, false},
		{"approval:abc", 0, false},
		{"approval:", 0, false},
		{"subagent:schedule", 0, false},
		{"negotiation:sess_123", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := TopicUserID(c.topic)
		if got != c.want || ok != c.ok {
			t.Fatalf("TopicUserID(%q) = (%d,%v), want (%d,%v)", c.topic, got, ok, c.want, c.ok)
		}
	}
}

type fakeResolver map[string]int64

func (f fakeResolver) ResolveUserID(topic string) (int64, bool) {
	id, ok := f[topic]
	return id, ok
}

func TestResolveTopicUserID(t *testing.T) {
	r := fakeResolver{"negotiation:sess_123": 88, "approval:1": 999}
	if id, ok := ResolveTopicUserID("approval:1", r); !ok || id != 1 {
		t.Fatalf("fixed prefix should win: got (%d,%v)", id, ok)
	}
	if id, ok := ResolveTopicUserID("negotiation:sess_123", r); !ok || id != 88 {
		t.Fatalf("resolver fallback: got (%d,%v)", id, ok)
	}
	if _, ok := ResolveTopicUserID("negotiation:unknown", r); ok {
		t.Fatalf("unknown topic should not resolve")
	}
	if _, ok := ResolveTopicUserID("subagent:x", nil); ok {
		t.Fatalf("nil resolver should not resolve")
	}
}
