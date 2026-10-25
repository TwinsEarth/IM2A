package id

import (
	"regexp"
	"strings"
	"testing"
)

func TestUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for i := 0; i < 1000; i++ {
		u := UUIDv4()
		if !re.MatchString(u) {
			t.Fatalf("invalid uuid v4: %s", u)
		}
		if seen[u] {
			t.Fatalf("duplicate uuid: %s", u)
		}
		seen[u] = true
		if strings.Count(u, "-") != 4 {
			t.Fatalf("uuid dash count: %s", u)
		}
	}
}
