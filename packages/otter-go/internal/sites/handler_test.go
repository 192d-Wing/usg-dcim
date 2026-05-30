package sites

import "testing"

func TestStrPtr(t *testing.T) {
	if strPtr("") != nil {
		t.Error("empty should be nil")
	}
	v := strPtr("x")
	if v == nil || *v != "x" {
		t.Errorf("got %v want pointer to x", v)
	}
}
