package resolvconf

import "testing"

func TestParseNameservers(t *testing.T) {
	text := `
# comment
nameserver 1.1.1.1
nameserver 8.8.8.8
nameserver 2001:4860:4860::8888
nameserver 1.1.1.1
search local
`
	got := Parse(text)
	if len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "8.8.8.8" {
		t.Fatalf("got %v", got)
	}
}

func TestParseEmpty(t *testing.T) {
	if got := Parse(""); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
