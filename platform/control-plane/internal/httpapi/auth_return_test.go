package httpapi

import "testing"

func TestSignInReturnsOnlyToPagesOfThisWebUI(t *testing.T) {
	for value, want := range map[string]string{
		"/admin/audit":          "/admin/audit",
		"/repositories/x?tab=1": "/repositories/x?tab=1",
		"":                      "/",
		"admin":                 "/",
		"//evil.example/":       "/",
		"/\\evil.example":       "/",
		"https://evil.example/": "/",
		"/web/v1/logout":        "/",
		"/a\r\nSet-Cookie: x":   "/",
	} {
		if got := returnPath(value); got != want {
			t.Errorf("returnPath(%q) = %q, want %q", value, got, want)
		}
	}
}
