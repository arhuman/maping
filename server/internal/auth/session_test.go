package auth

import (
	"strings"
	"testing"
	"time"
)

// testKey is a fixed 32-byte session key for the signer tests.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestNewSignerRejectsShortKey(t *testing.T) {
	if _, err := newSigner([]byte("too-short")); err == nil {
		t.Fatal("expected error for < 32 byte key")
	}
	if _, err := newSigner(testKey); err != nil {
		t.Fatalf("32-byte key rejected: %v", err)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s, err := newSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	sess := Session{OrgID: "org-1", MemberID: "mem-1", Role: "admin", Exp: now.Add(time.Hour)}
	got, err := s.decode(s.encode(sess), now)
	if err != nil {
		t.Fatalf("decode valid session: %v", err)
	}
	if got.OrgID != "org-1" || got.MemberID != "mem-1" || got.Role != "admin" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.Exp.Equal(sess.Exp) {
		t.Errorf("exp mismatch: got %v want %v", got.Exp, sess.Exp)
	}
}

func TestSessionExpired(t *testing.T) {
	s, _ := newSigner(testKey)
	now := time.Unix(1_700_000_000, 0)
	sess := Session{OrgID: "org-1", Exp: now.Add(-time.Second)}
	if _, err := s.decode(s.encode(sess), now); err != errBadSession {
		t.Fatalf("expired session must be rejected, got %v", err)
	}
}

func TestSessionTamperedSignature(t *testing.T) {
	s, _ := newSigner(testKey)
	now := time.Unix(1_700_000_000, 0)
	enc := s.encode(Session{OrgID: "org-1", Exp: now.Add(time.Hour)})
	// Flip a byte in the MAC portion (after the dot).
	dot := strings.IndexByte(enc, '.')
	tampered := enc[:dot+1] + "AAAA" + enc[dot+5:]
	if _, err := s.decode(tampered, now); err != errBadSession {
		t.Fatalf("tampered signature must be rejected, got %v", err)
	}
}

func TestSessionWrongKey(t *testing.T) {
	s1, _ := newSigner(testKey)
	s2, _ := newSigner([]byte("ffffffffffffffffffffffffffffffff"))
	now := time.Unix(1_700_000_000, 0)
	enc := s1.encode(Session{OrgID: "org-1", Exp: now.Add(time.Hour)})
	if _, err := s2.decode(enc, now); err != errBadSession {
		t.Fatalf("wrong key must be rejected, got %v", err)
	}
}

func TestSessionMalformed(t *testing.T) {
	s, _ := newSigner(testKey)
	now := time.Unix(1_700_000_000, 0)
	for _, v := range []string{"", "nodot", "!!!.@@@", "onlyonepart."} {
		if _, err := s.decode(v, now); err != errBadSession {
			t.Errorf("malformed %q must be rejected, got %v", v, err)
		}
	}
}
