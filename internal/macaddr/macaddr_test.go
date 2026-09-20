package macaddr

import "testing"

func TestIsPrivate(t *testing.T) {
	cases := []struct {
		name string
		mac  string
		want bool
	}{
		{"registered OUI", "10:bb:cc:dd:ee:01", false},
		{"another registered-looking OUI", "00:1a:2b:33:44:55", false},
		{"locally administered bit set", "02:00:00:00:00:01", true},
		{"randomized-looking address", "8a:3c:1f:00:11:22", true},
		{"uppercase input", "8A:3C:1F:00:11:22", true},
		{"dash separators", "02-00-00-00-00-01", true},
		{"too short to determine", "a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsPrivate(c.mac); got != c.want {
				t.Errorf("IsPrivate(%q) = %v, want %v", c.mac, got, c.want)
			}
		})
	}
}
