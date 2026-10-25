package version

import "testing"

func TestVersionSemVer(t *testing.T) {
	if Version == "" || Version[0] != 'v' {
		t.Fatalf("version must be v-prefixed semver, got %q", Version)
	}
	if len(Version) < 3 {
		t.Fatalf("version too short: %q", Version)
	}
}
