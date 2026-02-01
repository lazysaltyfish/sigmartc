package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeNickname(t *testing.T) {
	name, err := normalizeNickname("  alice  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "alice" {
		t.Fatalf("expected trimmed nickname, got %q", name)
	}

	if _, err := normalizeNickname(""); err == nil {
		t.Fatal("expected error for empty nickname")
	}

	longName := strings.Repeat("a", maxNicknameRune+1)
	if _, err := normalizeNickname(longName); err == nil {
		t.Fatal("expected error for too-long nickname")
	}
}

func TestStripPort(t *testing.T) {
	cases := map[string]string{
		"example.com:443": "example.com",
		"example.com":     "example.com",
		"[::1]:8443":      "::1",
		"[::1]":           "::1",
	}

	for input, want := range cases {
		if got := stripPort(input); got != want {
			t.Fatalf("stripPort(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRequestHost(t *testing.T) {
	req := &http.Request{
		Host: "origin.example.com:8443",
		Header: http.Header{
			"X-Forwarded-Host": []string{"forwarded.example.com:443, proxy.example.com"},
		},
	}
	if got := requestHost(req); got != "forwarded.example.com" {
		t.Fatalf("expected forwarded host, got %q", got)
	}

	req.Header = http.Header{}
	if got := requestHost(req); got != "origin.example.com" {
		t.Fatalf("expected origin host, got %q", got)
	}
}

func TestCheckWSOrigin(t *testing.T) {
	req := &http.Request{
		Host: "example.com",
		Header: http.Header{
			"Origin": []string{"https://example.com"},
		},
	}
	if !checkWSOrigin(req) {
		t.Fatal("expected origin to be accepted when host matches")
	}

	req.Header.Set("Origin", "https://evil.com")
	if checkWSOrigin(req) {
		t.Fatal("expected origin to be rejected when host mismatches")
	}

	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	if checkWSOrigin(req) {
		t.Fatal("expected origin to be rejected when proto mismatches forwarded proto")
	}

	req.Header = http.Header{}
	if !checkWSOrigin(req) {
		t.Fatal("expected empty origin to be accepted")
	}
}

func TestClientIP(t *testing.T) {
	req := &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header: http.Header{
			"X-Real-Ip": []string{"203.0.113.5"},
		},
	}
	if got := clientIP(req); got != "203.0.113.5" {
		t.Fatalf("expected X-Real-IP to be used, got %q", got)
	}

	req = &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header: http.Header{
			"X-Forwarded-For": []string{"bad-ip, 198.51.100.7"},
		},
	}
	if got := clientIP(req); got != "198.51.100.7" {
		t.Fatalf("expected X-Forwarded-For to be used, got %q", got)
	}

	req = &http.Request{
		RemoteAddr: "8.8.8.8:1234",
		Header: http.Header{
			"X-Real-Ip":       []string{"203.0.113.9"},
			"X-Forwarded-For": []string{"198.51.100.9"},
		},
	}
	if got := clientIP(req); got != "8.8.8.8" {
		t.Fatalf("expected remote addr to be used for untrusted proxy, got %q", got)
	}
}
