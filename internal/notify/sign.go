package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
)

// Webhook signature headers.
const (
	HeaderTimestamp = "X-Spinneret-Timestamp"
	HeaderSignature = "X-Spinneret-Signature"
)

// WebhookSignature returns the value of the X-Spinneret-Signature header:
// "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)), where
// timestamp is the X-Spinneret-Timestamp header value (Unix seconds).
// Receivers recompute it over the raw request body to authenticate deliveries.
func WebhookSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// feishuSign computes the Feishu custom bot signature:
// base64(HMAC-SHA256(key = timestamp + "\n" + secret, message = "")),
// timestamp in Unix seconds.
func feishuSign(secret string, timestamp int64) string {
	key := strconv.FormatInt(timestamp, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// dingTalkSign computes the DingTalk robot signature (before URL encoding):
// base64(HMAC-SHA256(key = secret, message = timestamp + "\n" + secret)),
// timestamp in Unix milliseconds.
func dingTalkSign(secret string, timestampMs int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMs, 10) + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
