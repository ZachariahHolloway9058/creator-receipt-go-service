package receipt

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordingSender struct {
	calls int
	key   string
	mail  Email
}

func (s *recordingSender) Send(_ context.Context, key string, mail Email) (string, error) {
	s.calls++
	s.key = key
	s.mail = mail
	return "msg_123", nil
}

func TestReceiptDeliveryDecision(t *testing.T) {
	ready := Order{
		ID:                "ord_42",
		BuyerEmail:        "buyer@example.com",
		AssetTitle:        "Studio Light Presets",
		AmountCents:       2400,
		Currency:          "USD",
		PaymentStatus:     "paid",
		ProcessingStatus:  "ready",
		SignedDownloadURL: "https://downloads.example.com/signed/ord_42",
		SubscriberUpdates: true,
	}

	tests := []struct {
		name      string
		mutate    func(*Order)
		wantSend  bool
		wantState string
	}{
		{name: "ready asset sends receipt", mutate: func(*Order) {}, wantSend: true, wantState: "receipt_sent"},
		{name: "processing asset waits", mutate: func(o *Order) { o.ProcessingStatus = "processing" }},
		{name: "unpaid order waits", mutate: func(o *Order) { o.PaymentStatus = "pending" }},
		{name: "missing download waits", mutate: func(o *Order) { o.SignedDownloadURL = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := ready
			tt.mutate(&order)
			mail := &recordingSender{}
			got, err := NewReceiptSender(mail).Deliver(context.Background(), order)

			if tt.wantSend {
				if err != nil {
					t.Fatalf("Deliver() error = %v", err)
				}
				if mail.calls != 1 || mail.key != order.ID {
					t.Fatalf("Send() calls = %d, key = %q", mail.calls, mail.key)
				}
				if got.State != tt.wantState || got.MessageID != "msg_123" {
					t.Fatalf("delivery = %+v", got)
				}
				if mail.mail.To != order.BuyerEmail || !strings.Contains(mail.mail.HTML, order.SignedDownloadURL) {
					t.Fatalf("email boundary = %+v", mail.mail)
				}
				if !strings.Contains(mail.mail.HTML, "creator updates") {
					t.Fatal("subscriber update choice missing from receipt")
				}
				return
			}

			if !errors.Is(err, ErrNotReady) {
				t.Fatalf("Deliver() error = %v, want ErrNotReady", err)
			}
			if mail.calls != 0 {
				t.Fatalf("Send() calls = %d, want 0", mail.calls)
			}
		})
	}
}
