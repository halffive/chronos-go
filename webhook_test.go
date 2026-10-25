package chronos

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Signed with the Node.js SDK's scheme; changing it breaks the proof that both SDKs agree.
const (
	webhookSecret     = "test_secret_key"
	webhookDeliveryID = "exec_abc123"
	webhookTimestamp  = "1768471200"
	webhookBody       = `{"job_id":"job_xyz","handler":"send-email","execution_id":"exec_abc123","scheduled_for":"2026-01-15T10:00:00Z","attempt":1,"payload":{"to":"user@example.com"}}`
	webhookSignature  = "sha256=a9399db18d020068facc2f7ec12f8a627279ad589092aaebbc11a8873cb74b2b"
)

var webhookNow = time.Unix(1768471200, 0)

func webhookHeader(signature, timestamp, deliveryID string) http.Header {
	h := http.Header{}
	h.Set("X-Chronos-Signature", signature)
	h.Set("X-Chronos-Timestamp", timestamp)
	h.Set("X-Chronos-Delivery-Id", deliveryID)
	return h
}

func testWebhook(t *testing.T, secrets []string, opts ...WebhookOption) *Webhook {
	t.Helper()
	w, err := NewWebhook(secrets, opts...)
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return webhookNow }
	return w
}

func sign(secret, deliveryID, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(deliveryID + "." + timestamp + "." + body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookAcceptsTheSharedVector(t *testing.T) {
	w := testWebhook(t, []string{webhookSecret})
	if err := w.Verify([]byte(webhookBody), webhookHeader(webhookSignature, webhookTimestamp, webhookDeliveryID)); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookAcceptsUppercaseHex(t *testing.T) {
	w := testWebhook(t, []string{webhookSecret})
	upper := "sha256=" + strings.ToUpper(strings.TrimPrefix(webhookSignature, "sha256="))
	if err := w.Verify([]byte(webhookBody), webhookHeader(upper, webhookTimestamp, webhookDeliveryID)); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookAcceptsAnySecretDuringRotation(t *testing.T) {
	w := testWebhook(t, []string{"", "wrong_secret", "  " + webhookSecret + "  "})
	if err := w.Verify([]byte(webhookBody), webhookHeader(webhookSignature, webhookTimestamp, webhookDeliveryID)); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookRejections(t *testing.T) {
	stale := "1768470899"
	tests := []struct {
		name   string
		body   string
		header http.Header
		reason string
	}{
		{name: "missing signature", body: webhookBody, header: webhookHeader("", webhookTimestamp, webhookDeliveryID), reason: "missing header x-chronos-signature"},
		{name: "missing delivery id", body: webhookBody, header: webhookHeader(webhookSignature, webhookTimestamp, ""), reason: "missing header x-chronos-delivery-id"},
		{name: "non-numeric timestamp", body: webhookBody, header: webhookHeader(webhookSignature, "0x1a", webhookDeliveryID), reason: "invalid timestamp"},
		{name: "zero timestamp", body: webhookBody, header: webhookHeader(webhookSignature, "0", webhookDeliveryID), reason: "invalid timestamp"},
		{name: "huge timestamp", body: webhookBody, header: webhookHeader(webhookSignature, "99999999999999999999", webhookDeliveryID), reason: "invalid timestamp"},
		{name: "stale timestamp", body: webhookBody, header: webhookHeader(sign(webhookSecret, webhookDeliveryID, stale, webhookBody), stale, webhookDeliveryID), reason: "timestamp outside tolerance"},
		{name: "far-future timestamp", body: webhookBody, header: webhookHeader(webhookSignature, "9223372036", webhookDeliveryID), reason: "timestamp outside tolerance"},
		{name: "timestamp past the Duration range", body: webhookBody, header: webhookHeader(webhookSignature, "11068471200", webhookDeliveryID), reason: "timestamp outside tolerance"},
		{name: "missing prefix", body: webhookBody, header: webhookHeader(strings.TrimPrefix(webhookSignature, "sha256="), webhookTimestamp, webhookDeliveryID), reason: "signature must be sha256=<hex>"},
		{name: "empty digest", body: webhookBody, header: webhookHeader("sha256=", webhookTimestamp, webhookDeliveryID), reason: "signature is not a SHA-256 digest"},
		{name: "malformed hex", body: webhookBody, header: webhookHeader("sha256=zzzz_not_hex", webhookTimestamp, webhookDeliveryID), reason: "signature is not valid hex"},
		{name: "short digest", body: webhookBody, header: webhookHeader("sha256=abcd", webhookTimestamp, webhookDeliveryID), reason: "signature is not a SHA-256 digest"},
		{name: "tampered body", body: `{"tampered":true}`, header: webhookHeader(webhookSignature, webhookTimestamp, webhookDeliveryID), reason: "signature does not match"},
	}
	w := testWebhook(t, []string{webhookSecret})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := w.Verify([]byte(tt.body), tt.header)
			if !errors.Is(err, ErrInvalidWebhook) {
				t.Fatalf("error = %v, want ErrInvalidWebhook", err)
			}
			if !strings.HasSuffix(err.Error(), tt.reason) {
				t.Errorf("error = %q, want reason %q", err, tt.reason)
			}
		})
	}
}

func TestWebhookToleranceBoundaryIsAccepted(t *testing.T) {
	edge := "1768470900"
	w := testWebhook(t, []string{webhookSecret})
	if err := w.Verify([]byte(webhookBody), webhookHeader(sign(webhookSecret, webhookDeliveryID, edge, webhookBody), edge, webhookDeliveryID)); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookZeroToleranceSkipsTheTimestampCheck(t *testing.T) {
	old := "1000000000"
	w := testWebhook(t, []string{webhookSecret}, WithTimestampTolerance(0))
	if err := w.Verify([]byte(webhookBody), webhookHeader(sign(webhookSecret, webhookDeliveryID, old, webhookBody), old, webhookDeliveryID)); err != nil {
		t.Fatal(err)
	}
}

func TestNewWebhookRejectsBadConfig(t *testing.T) {
	if _, err := NewWebhook([]string{"", "  "}); err == nil {
		t.Error("blank secrets: want an error")
	}
	if _, err := NewWebhook([]string{webhookSecret}, WithTimestampTolerance(-time.Second)); err == nil {
		t.Error("negative tolerance: want an error")
	}
}
