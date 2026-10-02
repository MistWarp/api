// Package roturwebhook checks the signature on webhooks from Rotur Apps.
package roturwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Tolerance is how far a delivery's timestamp may be from now, in seconds,
// so an old delivery can't be replayed.
const Tolerance = 300

// Sign is the Rotur-Signature value for a delivery: v1= and the hex
// HMAC-SHA256 of "<id>.<timestamp>.<body>", keyed with the whole secret.
func Sign(secret, id, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id + "." + timestamp + "." + body))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether a delivery came from Rotur: it is signed with
// secret, and its timestamp is within Tolerance of now.
func Verify(secret, id, timestamp, body, signature string) bool {
	return VerifyAt(secret, id, timestamp, body, signature, time.Now().Unix())
}

// VerifyAt is Verify at a given time, in Unix seconds.
func VerifyAt(secret, id, timestamp, body, signature string, now int64) bool {
	if secret == "" || id == "" || signature == "" {
		return false
	}
	sent, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || sent < now-Tolerance || sent > now+Tolerance {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(Sign(secret, id, timestamp, body)))
}
