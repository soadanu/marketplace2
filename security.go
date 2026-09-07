package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// newID returns a random hex ID. Good enough for a project this size;
// swap for a UUID library later if you want stricter guarantees.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

const hashIterations = 100_000

// hashPassword derives a salted hash using HMAC-SHA256 in a loop (a simple
// PBKDF2). No external crypto dependency needed - stdlib only.
func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	derived := pbkdf2HMAC(password, salt, hashIterations)
	return fmt.Sprintf("%s$%s", hex.EncodeToString(salt), hex.EncodeToString(derived))
}

func verifyPassword(password, stored string) bool {
	parts := strings.SplitN(stored, "$", 2)
	if len(parts) != 2 {
		return false
	}
	saltHex, hashHex := parts[0], parts[1]
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(hashHex)
	if err != nil {
		return false
	}
	got := pbkdf2HMAC(password, salt, hashIterations)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2HMAC(password string, salt []byte, iterations int) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write(salt)
	result := mac.Sum(nil)
	for i := 1; i < iterations; i++ {
		mac.Reset()
		mac.Write(result)
		result = mac.Sum(nil)
	}
	return result
}
