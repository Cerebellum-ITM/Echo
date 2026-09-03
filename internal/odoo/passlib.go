package odoo

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strings"
)

// Odoo hashes passwords with passlib's pbkdf2_sha512 at its default cost,
// and its crypt context verifies that scheme natively — so a hash we build
// here logs in without Odoo rewriting it.
const (
	pbkdf2Rounds  = 25000
	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 64
)

// HashPassword returns a passlib-format pbkdf2_sha512 hash
// ($pbkdf2-sha512$<rounds>$<salt>$<checksum>) that Odoo's res_users
// accepts as-is, so the plaintext never reaches the database.
func HashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	return hashWithSalt(password, salt)
}

func hashWithSalt(password string, salt []byte) (string, error) {
	key, err := pbkdf2.Key(sha512.New, password, salt, pbkdf2Rounds, pbkdf2KeyLen)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	return fmt.Sprintf("$pbkdf2-sha512$%d$%s$%s", pbkdf2Rounds, adaptedB64(salt), adaptedB64(key)), nil
}

// adaptedB64 is passlib's base64 variant: unpadded, with '+' written as
// '.'. Reproducing it is what makes the hash parseable by passlib.
func adaptedB64(b []byte) string {
	return strings.ReplaceAll(base64.RawStdEncoding.EncodeToString(b), "+", ".")
}
