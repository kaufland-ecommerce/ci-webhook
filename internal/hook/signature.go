package hook

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"time"
)

// SignatureError describes an invalid payload signature passed to Hook.
type SignatureError struct {
	Signature  string
	Signatures []string

	emptyPayload bool
}

func (e *SignatureError) Error() string {
	if e == nil {
		return "<nil>"
	}

	var empty string
	if e.emptyPayload {
		empty = " on empty payload"
	}

	if e.Signatures != nil {
		return fmt.Sprintf("invalid payload signatures %s%s", e.Signatures, empty)
	}

	return fmt.Sprintf("invalid payload signature %s%s", e.Signature, empty)
}

// IsSignatureError returns whether err is of type SignatureError.
func IsSignatureError(err error) bool {
	switch err.(type) {
	case *SignatureError:
		return true
	default:
		return false
	}
}

// ValidateMAC will verify that the expected mac for the given hash will match
// the one provided.
func ValidateMAC(payload []byte, mac hash.Hash, signatures []string) (string, error) {
	// Write the payload to the provided hash.
	_, err := mac.Write(payload)
	if err != nil {
		return "", err
	}

	actualMAC := hex.EncodeToString(mac.Sum(nil))

	for _, signature := range signatures {
		if hmac.Equal([]byte(signature), []byte(actualMAC)) {
			return actualMAC, err
		}
	}

	e := &SignatureError{Signatures: signatures}
	if len(payload) == 0 {
		e.emptyPayload = true
	}

	return actualMAC, e
}

// CheckPayloadSignature calculates and verifies SHA1 signature of the given payload
func CheckPayloadSignature(payload []byte, secret, signature string) (string, error) {
	if secret == "" {
		return "", errors.New("signature validation secret can not be empty")
	}

	// Extract the signatures.
	signatures := ExtractSignatures(signature, "sha1=")

	// Validate the MAC.
	return ValidateMAC(payload, hmac.New(sha1.New, []byte(secret)), signatures)
}

// CheckPayloadSignature256 calculates and verifies SHA256 signature of the given payload
func CheckPayloadSignature256(payload []byte, secret, signature string) (string, error) {
	if secret == "" {
		return "", errors.New("signature validation secret can not be empty")
	}

	// Extract the signatures.
	signatures := ExtractSignatures(signature, "sha256=")

	// Validate the MAC.
	return ValidateMAC(payload, hmac.New(sha256.New, []byte(secret)), signatures)
}

// CheckPayloadSignature512 calculates and verifies SHA512 signature of the given payload
func CheckPayloadSignature512(payload []byte, secret, signature string) (string, error) {
	if secret == "" {
		return "", errors.New("signature validation secret can not be empty")
	}

	// Extract the signatures.
	signatures := ExtractSignatures(signature, "sha512=")

	// Validate the MAC.
	return ValidateMAC(payload, hmac.New(sha512.New, []byte(secret)), signatures)
}

func CheckScalrSignature(r *Request, signingKey string, checkDate bool) (bool, error) {
	if r.Headers == nil {
		return false, nil
	}

	// Check for the signature and date headers
	if _, ok := r.Headers["X-Signature"]; !ok {
		return false, nil
	}
	if _, ok := r.Headers["Date"]; !ok {
		return false, nil
	}
	if signingKey == "" {
		return false, errors.New("signature validation signing key can not be empty")
	}

	providedSignature := r.Headers["X-Signature"].(string)
	dateHeader := r.Headers["Date"].(string)
	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write(r.Body)
	mac.Write([]byte(dateHeader))
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(providedSignature), []byte(expectedSignature)) {
		return false, &SignatureError{Signature: providedSignature}
	}

	if !checkDate {
		return true, nil
	}
	// Example format: Fri 08 Sep 2017 11:24:32 UTC
	date, err := time.Parse("Mon 02 Jan 2006 15:04:05 MST", dateHeader)
	if err != nil {
		return false, err
	}
	now := time.Now()
	delta := math.Abs(now.Sub(date).Seconds())

	if delta > 300 {
		return false, &SignatureError{Signature: "outdated"}
	}
	return true, nil
}
