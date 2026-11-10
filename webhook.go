package chronos

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultWebhookTolerance is how far a push delivery's timestamp may be from
// now before [Webhook.Verify] rejects it.
const DefaultWebhookTolerance = 5 * time.Minute

const (
	headerSignature  = "X-Chronos-Signature"
	headerTimestamp  = "X-Chronos-Timestamp"
	headerDeliveryID = "X-Chronos-Delivery-Id"
	signaturePrefix  = "sha256="
)

// PushedJob is the body of a push delivery. Decode it after [Webhook.Verify]
// accepts the request and [Webhook.IsTestRequest] returns false.
type PushedJob struct {
	// Type is always "job".
	Type    string  `json:"type"`
	JobID   string  `json:"job_id"`
	Handler *string `json:"handler"`
	// ExecutionID equals the X-Chronos-Delivery-Id header. Each attempt gets
	// a new one.
	ExecutionID  string    `json:"execution_id"`
	ScheduledFor time.Time `json:"scheduled_for"`
	// Attempt starts at 1 and restarts at 1 after a manual retry.
	Attempt int `json:"attempt"`
	// Payload is the job's payload as sent. For a job without a payload,
	// Chronos sends JSON null, which decodes to json.RawMessage("null"), not
	// nil.
	Payload json.RawMessage `json:"payload"`
}

// Webhook verifies the signature on push deliveries. It is safe for
// concurrent use.
type Webhook struct {
	secrets   [][]byte
	tolerance time.Duration
	now       func() time.Time
}

// WebhookOption configures a [Webhook].
type WebhookOption func(*Webhook) error

// WithTimestampTolerance sets how far a delivery's timestamp may be from now.
// 0 turns the check off, which also removes replay protection. Defaults to
// [DefaultWebhookTolerance].
func WithTimestampTolerance(tolerance time.Duration) WebhookOption {
	return func(w *Webhook) error {
		if tolerance < 0 {
			return fmt.Errorf("chronos: timestamp tolerance must not be negative, got %s", tolerance)
		}
		w.tolerance = tolerance
		return nil
	}
}

// NewWebhook returns a verifier for the workspace's signing secrets. Pass
// more than one during a rotation; a delivery signed with any of them is
// accepted. Blank secrets are ignored.
func NewWebhook(secrets []string, opts ...WebhookOption) (*Webhook, error) {
	w := &Webhook{tolerance: DefaultWebhookTolerance, now: time.Now}
	for _, s := range secrets {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			w.secrets = append(w.secrets, []byte(trimmed))
		}
	}
	if len(w.secrets) == 0 {
		return nil, errors.New("chronos: at least one non-empty signing secret is required")
	}
	for _, opt := range opts {
		if err := opt(w); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// Verify checks that body was signed by Chronos and is recent. Pass the raw
// request body, not a re-encoded one, and the request's http.Header. Every
// failure wraps [ErrInvalidWebhook].
func (w *Webhook) Verify(body []byte, header http.Header) error {
	for _, name := range []string{headerSignature, headerTimestamp, headerDeliveryID} {
		if header.Get(name) == "" {
			return invalidWebhook("missing header " + strings.ToLower(name))
		}
	}
	signature := header.Get(headerSignature)
	timestamp := header.Get(headerTimestamp)
	deliveryID := header.Get(headerDeliveryID)

	signedAt, err := parseWebhookTimestamp(timestamp)
	if err != nil {
		return err
	}
	if w.tolerance > 0 && !w.withinTolerance(signedAt) {
		return invalidWebhook("timestamp outside tolerance")
	}

	expected, err := decodeSignature(signature)
	if err != nil {
		return err
	}
	for _, secret := range w.secrets {
		mac := hmac.New(sha256.New, secret)
		fmt.Fprintf(mac, "%s.%s.", deliveryID, timestamp)
		mac.Write(body)
		if hmac.Equal(mac.Sum(nil), expected) {
			return nil
		}
	}
	return invalidWebhook("signature does not match")
}

// IsTestRequest reports whether a body that [Webhook.Verify] accepted is a test
// request. The dashboard sends one from the Test button next to a push URL and
// from a webhook alert channel's Send test button. Answer it with a 2xx without
// running any work. It returns false for a body that is not a JSON object.
func (w *Webhook) IsTestRequest(body []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return false
	}
	return event.Type == "test"
}

func (w *Webhook) withinTolerance(signedAt time.Time) bool {
	age := w.now().Sub(signedAt)
	return age <= w.tolerance && age >= -w.tolerance
}

func parseWebhookTimestamp(timestamp string) (time.Time, error) {
	if !isAllDigits(timestamp) {
		return time.Time{}, invalidWebhook("invalid timestamp")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, invalidWebhook("invalid timestamp")
	}
	return time.Unix(seconds, 0), nil
}

func decodeSignature(signature string) ([]byte, error) {
	digest, ok := strings.CutPrefix(signature, signaturePrefix)
	if !ok {
		return nil, invalidWebhook("signature must be sha256=<hex>")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil {
		return nil, invalidWebhook("signature is not valid hex")
	}
	if len(decoded) != sha256.Size {
		return nil, invalidWebhook("signature is not a SHA-256 digest")
	}
	return decoded, nil
}

func invalidWebhook(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidWebhook, reason)
}
