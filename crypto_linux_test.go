package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
	"testing"
)

// encryptForTest writes a value the way Chromium does: PKCS#7 padding,
// AES-128-CBC with an IV of spaces, and the version prefix in front.
func encryptForTest(t *testing.T, prefix, password string, plain []byte) []byte {
	t.Helper()
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	return encryptRawForTest(t, prefix, password, padded)
}

// encryptRawForTest encrypts padded exactly as given, so a test can produce
// bad padding on purpose.
func encryptRawForTest(t *testing.T, prefix, password string, padded []byte) []byte {
	t.Helper()
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{' '}, aes.BlockSize)).CryptBlocks(out, padded)
	return append([]byte(prefix), out...)
}

// setKeyringPassword swaps the keyring reader for one test and restores it after.
func setKeyringPassword(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := keyringPassword
	keyringPassword = fn
	forgetKeyringPassword()
	t.Cleanup(func() {
		keyringPassword = orig
		forgetKeyringPassword()
	})
}

var testPlain = []byte(`{"acct:x|y":{"token":"t","expiresAt":1}}`)

func TestDecryptV10(t *testing.T) {
	// v10 must never touch the keyring, so fail the test if it does.
	setKeyringPassword(t, func() (string, error) {
		t.Error("keyring consulted for a v10 value")
		return "", errors.New("unexpected")
	})
	tests := []struct {
		name  string
		plain []byte
	}{
		{"json", testPlain},
		{"empty", []byte{}},
		{"exact block", []byte("0123456789abcdef")},
		{"longer than a block", bytes.Repeat([]byte("x"), 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decryptValue(encryptForTest(t, "v10", "peanuts", tt.plain))
			if err != nil {
				t.Fatalf("decryptValue: %v", err)
			}
			if !bytes.Equal(got, tt.plain) {
				t.Fatalf("got %q, want %q", got, tt.plain)
			}
		})
	}
}

func TestDecryptV11Keyring(t *testing.T) {
	setKeyringPassword(t, func() (string, error) { return "secret-pass", nil })
	got, err := decryptValue(encryptForTest(t, "v11", "secret-pass", testPlain))
	if err != nil {
		t.Fatalf("decryptValue: %v", err)
	}
	if !bytes.Equal(got, testPlain) {
		t.Fatalf("got %q, want %q", got, testPlain)
	}
}

func TestDecryptV11EmptyPasswordFallback(t *testing.T) {
	// The keyring gives a password that does not match, so the value must be
	// decrypted with the empty password instead.
	setKeyringPassword(t, func() (string, error) { return "wrong-pass", nil })
	got, err := decryptValue(encryptForTest(t, "v11", "", testPlain))
	if err != nil {
		t.Fatalf("decryptValue: %v", err)
	}
	if !bytes.Equal(got, testPlain) {
		t.Fatalf("got %q, want %q", got, testPlain)
	}
}

func TestDecryptV11NoKeyMatches(t *testing.T) {
	setKeyringPassword(t, func() (string, error) { return "wrong-pass", nil })
	if _, err := decryptValue(encryptForTest(t, "v11", "other-pass", testPlain)); err == nil {
		t.Fatal("expected an error when neither key matches")
	}
}

func TestDecryptV11KeyringError(t *testing.T) {
	setKeyringPassword(t, func() (string, error) { return "", errors.New("no keyring") })
	if _, err := decryptValue(encryptForTest(t, "v11", "", testPlain)); err == nil {
		t.Fatal("expected an error when the keyring cannot be read")
	}
}

func TestKeyringPasswordCached(t *testing.T) {
	calls := 0
	setKeyringPassword(t, func() (string, error) {
		calls++
		return "secret-pass", nil
	})
	value := encryptForTest(t, "v11", "secret-pass", testPlain)
	for i := 0; i < 3; i++ {
		if _, err := decryptValue(value); err != nil {
			t.Fatalf("decryptValue %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("keyring read %d times, want 1", calls)
	}
}

func TestDecryptValueErrors(t *testing.T) {
	setKeyringPassword(t, func() (string, error) {
		t.Error("keyring consulted for an invalid value")
		return "", errors.New("unexpected")
	})
	tests := []struct {
		name string
		data []byte
	}{
		{"nil", nil},
		{"prefix only", []byte("v10")},
		{"short", []byte("v10abc")},
		{"unknown prefix", encryptForTest(t, "v12", "peanuts", testPlain)},
		{"bad length", append([]byte("v10"), bytes.Repeat([]byte{1}, 17)...)},
		{"zero padding", encryptRawForTest(t, "v10", "peanuts", make([]byte, 16))},
		{"padding longer than block", encryptRawForTest(t, "v10", "peanuts", bytes.Repeat([]byte{0x11}, 16))},
		{"inconsistent padding", encryptRawForTest(t, "v10", "peanuts", append(bytes.Repeat([]byte{3}, 14), 3, 2))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decryptValue(tt.data); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// A locked keyring must not be asked to unlock on every usage check.
func TestKeyringFailureRemembered(t *testing.T) {
	calls := 0
	setKeyringPassword(t, func() (string, error) {
		calls++
		return "", errors.New("locked")
	})
	value := encryptForTest(t, "v11", "secret-pass", testPlain)
	for i := 0; i < 3; i++ {
		if _, err := decryptValue(value); err == nil {
			t.Fatal("expected an error while the keyring is locked")
		}
	}
	if calls != 1 {
		t.Fatalf("keyring read %d times, want 1", calls)
	}
	keyringErrAt = keyringErrAt.Add(-keyringRetry)
	_, _ = decryptValue(value)
	if calls != 2 {
		t.Fatalf("keyring read %d times after the retry window, want 2", calls)
	}
}
