package registry

import "testing"

func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"Owen's iPhone":       "owen-s-iphone",
		"  Living Room TV  ":  "living-room-tv",
		"already-clean":       "already-clean",
		"a--b---c":            "a-b-c",
		"-leading-trailing-":  "leading-trailing",
		"UPPER_CASE!!":        "upper-case",
		"":                    "",
	}
	for in, want := range cases {
		if got := SanitizeLabel(in); got != want {
			t.Errorf("SanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeLabelTruncatesTo63(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := SanitizeLabel(long)
	if len(got) > 63 {
		t.Errorf("expected len <= 63, got %d", len(got))
	}
}

func TestResolvePrefersOverride(t *testing.T) {
	override := "custom-name"
	client := makeClient("aa:bb:cc:dd:ee:ff", "Some Device", "some-hostname")
	got := Resolve(client, &override, "{vendor}-{macsuffix}")
	if got != "custom-name" {
		t.Errorf("got %q, want %q", got, "custom-name")
	}
}

func TestResolvePrefersUniFiNameOverHostname(t *testing.T) {
	client := makeClient("aa:bb:cc:dd:ee:ff", "Kitchen Speaker", "esp32")
	got := Resolve(client, nil, "{vendor}-{macsuffix}")
	if got != "kitchen-speaker" {
		t.Errorf("got %q, want %q", got, "kitchen-speaker")
	}
}

func TestResolveFallsBackOnGenericName(t *testing.T) {
	client := makeClient("f4:f5:d8:11:22:33", "Unknown", "")
	got := Resolve(client, nil, "{vendor}-{macsuffix}")
	if got != "google-2233" {
		t.Errorf("got %q, want %q", got, "google-2233")
	}
}

func TestResolveFallsBackWhenBothBlank(t *testing.T) {
	client := makeClient("aa:bb:cc:11:22:33", "", "")
	got := Resolve(client, nil, "{vendor}-{macsuffix}")
	if got != "device-2233" {
		t.Errorf("got %q, want %q", got, "device-2233")
	}
}

func TestDisambiguate(t *testing.T) {
	taken := map[string]string{"laptop": "aa:bb:cc:dd:ee:01"}

	// Different MAC claiming the same name gets suffixed.
	got := Disambiguate("laptop", "aa:bb:cc:dd:ee:02", taken)
	if got != "laptop-ee02" {
		t.Errorf("got %q, want %q", got, "laptop-ee02")
	}

	// Same MAC re-claiming its own name is unaffected.
	got = Disambiguate("laptop", "aa:bb:cc:dd:ee:01", taken)
	if got != "laptop" {
		t.Errorf("got %q, want %q", got, "laptop")
	}

	// An unclaimed name passes through untouched.
	got = Disambiguate("desktop", "aa:bb:cc:dd:ee:03", taken)
	if got != "desktop" {
		t.Errorf("got %q, want %q", got, "desktop")
	}
}
