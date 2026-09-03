package odoo

import (
	"strings"
	"testing"
)

// Reference value produced by Python's hashlib.pbkdf2_hmac with the same
// parameters. If passlib's encoding is ever reproduced wrongly, the
// symptom is a login Odoo silently rejects — this catches it here instead.
func TestHashWithSaltMatchesReferenceVector(t *testing.T) {
	salt := make([]byte, pbkdf2SaltLen)
	for i := range salt {
		salt[i] = byte(i)
	}
	const want = "$pbkdf2-sha512$25000$AAECAwQFBgcICQoLDA0ODw$hzjCVDG9FELw2b5EEyih3Omxf8FRIQmndWjgpGGArUfNcobEKrNA0TCDqviQOUQRez/u2tkBEug55SdLU1CGvQ"

	got, err := hashWithSalt("admin", salt)
	if err != nil {
		t.Fatalf("hashWithSalt: %v", err)
	}
	if got != want {
		t.Errorf("hash mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestHashPasswordFormat(t *testing.T) {
	hash, err := HashPassword("s3cr3t")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 5 {
		t.Fatalf("got %d fields, want 5: %q", len(parts), hash)
	}
	if parts[1] != "pbkdf2-sha512" {
		t.Errorf("scheme = %q, want pbkdf2-sha512", parts[1])
	}
	if parts[2] != "25000" {
		t.Errorf("rounds = %q, want 25000", parts[2])
	}
	if strings.Contains(hash, "+") || strings.Contains(hash, "=") {
		t.Errorf("hash is not passlib-adapted base64: %q", hash)
	}
}

func TestHashPasswordSaltsEachCall(t *testing.T) {
	first, err := HashPassword("same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("same")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical: salt is not random")
	}
}
