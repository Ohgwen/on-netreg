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

func TestParse(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff", true},
		{"AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff", true},
		{"aabb.ccdd.eeff", "aa:bb:cc:dd:ee:ff", true},
		{"AABBCCDDEEFF", "aa:bb:cc:dd:ee:ff", true},
		{"  aa:bb:cc:dd:ee:ff  ", "aa:bb:cc:dd:ee:ff", true},
		{"", "", false},
		{"aa:bb:cc:dd:ee", "", false},
		{"aa:bb:cc:dd:ee:gg", "", false},
		{"aa:bb:cc:dd:ee:ff:00:11", "", false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Parse(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.wantOK)
		}
	}
}
