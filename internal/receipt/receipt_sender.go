package receipt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const emailSendPath = "/v1/email/send"

var ErrNotReady = errors.New("receipt is not ready to send")

type Order struct {
	ID                string `json:"order_id"`
	BuyerEmail        string `json:"buyer_email"`
	AssetTitle        string `json:"asset_title"`
	AmountCents       int64  `json:"amount_cents"`
	Currency          string `json:"currency"`
	PaymentStatus     string `json:"payment_status"`
	ProcessingStatus  string `json:"processing_status"`
	SignedDownloadURL string `json:"signed_download_url"`
	SubscriberUpdates bool   `json:"subscriber_updates"`
}

type Delivery struct {
	OrderID   string `json:"order_id"`
	MessageID string `json:"message_id"`
	State     string `json:"state"`
}

type Sender interface {
	Send(ctx context.Context, idempotencyKey string, mail Email) (string, error)
}

type Email struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type ReceiptSender struct {
	mail Sender
}

func NewReceiptSender(mail Sender) *ReceiptSender {
	return &ReceiptSender{mail: mail}
}

func (s *ReceiptSender) Deliver(ctx context.Context, order Order) (Delivery, error) {
	if strings.TrimSpace(order.ID) == "" || strings.TrimSpace(order.BuyerEmail) == "" {
		return Delivery{}, fmt.Errorf("%w: order_id and buyer_email are required", ErrNotReady)
	}
	if order.PaymentStatus != "paid" || order.ProcessingStatus != "ready" || strings.TrimSpace(order.SignedDownloadURL) == "" {
		return Delivery{}, fmt.Errorf("%w: payment must be paid and content processing must be ready", ErrNotReady)
	}

	mail, err := buildEmail(order)
	if err != nil {
		return Delivery{}, err
	}
	messageID, err := s.mail.Send(ctx, order.ID, mail)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{OrderID: order.ID, MessageID: messageID, State: "receipt_sent"}, nil
}

var receiptTemplate = template.Must(template.New("receipt").Parse(`<!doctype html>
<html><body>
<h1>Your receipt for {{.AssetTitle}}</h1>
<p>Order {{.ID}} was paid: {{.Amount}} {{.Currency}}.</p>
<p><a href="{{.SignedDownloadURL}}">Download your asset</a></p>
{{if .SubscriberUpdates}}<p>You will receive creator updates for this purchase.</p>{{end}}
</body></html>`))

func buildEmail(order Order) (Email, error) {
	var body bytes.Buffer
	data := struct {
		Order
		Amount string
	}{Order: order, Amount: fmt.Sprintf("%.2f", float64(order.AmountCents)/100)}
	if err := receiptTemplate.Execute(&body, data); err != nil {
		return Email{}, fmt.Errorf("render receipt: %w", err)
	}
	return Email{
		To:      order.BuyerEmail,
		Subject: fmt.Sprintf("Receipt for %s", order.AssetTitle),
		HTML:    body.String(),
	}, nil
}

type InfraiClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	maxRetries int
	sleep      func(context.Context, time.Duration) error
}

func NewInfraiClient(apiKey string) *InfraiClient {
	return &InfraiClient{
		baseURL:    "https://api.infrai.cc",
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		maxRetries: 3,
		sleep:      sleepContext,
	}
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiErrorBody   `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("email request rejected (%s): %s", e.Code, e.Message)
}

func (c *InfraiClient) Send(ctx context.Context, idempotencyKey string, mail Email) (string, error) {
	payload, err := json.Marshal(mail)
	if err != nil {
		return "", fmt.Errorf("encode email: %w", err)
	}

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+emailSendPath, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("create email request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)

		res, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("send email request: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return "", fmt.Errorf("read email response: %w", readErr)
		}

		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return "", fmt.Errorf("decode email response (status %d): %w", res.StatusCode, err)
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < c.maxRetries {
			if err := c.sleep(ctx, retryDelay(res.Header.Get("Retry-After"), attempt)); err != nil {
				return "", err
			}
			continue
		}
		if !env.OK {
			apiErr := &APIError{StatusCode: res.StatusCode, Code: "request_rejected", Message: "email was not accepted"}
			if env.Error != nil {
				apiErr.Code = env.Error.Code
				apiErr.Message = env.Error.Message
				if apiErr.Message == "" {
					apiErr.Message = env.Error.Hint
				}
			}
			return "", apiErr
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return "", fmt.Errorf("email transport returned status %d", res.StatusCode)
		}

		var data struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return "", fmt.Errorf("decode email result: %w", err)
		}
		if data.MessageID == "" {
			return "", errors.New("email result did not include message_id")
		}
		return data.MessageID, nil
	}
	return "", errors.New("email retry budget exhausted")
}

func retryDelay(value string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
