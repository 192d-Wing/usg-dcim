package sites

import "testing"

func TestParseInt32(t *testing.T) {
	cases := []struct {
		s        string
		def, want int32
	}{
		{"", 50, 50},
		{"100", 50, 100},
		{"-5", 50, 1},     // clamp low
		{"99999", 50, 500}, // clamp high
		{"not-a-number", 50, 50},
	}
	for _, c := range cases {
		got := parseInt32(c.s, c.def, 1, 500)
		if got != c.want {
			t.Errorf("parseInt32(%q,%d): got %d want %d", c.s, c.def, got, c.want)
		}
	}
}

func TestStrPtr(t *testing.T) {
	if strPtr("") != nil {
		t.Error("empty should be nil")
	}
	v := strPtr("x")
	if v == nil || *v != "x" {
		t.Errorf("got %v want pointer to x", v)
	}
}
