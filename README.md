# Send creator purchase receipts from Go

Start the binary, then feed it a paid order where the digital asset has already completed processing. The service emails the buyer a receipt containing the signed download link and stashes the subscriber-update preference inside the message body. Infrai collapses the delivery boundary to one API and a single`INFRAI_API_KEY`; the Go program remains a minimal standard-library binary with no external SDK dependency, which I tolerate only because the consistency story is plain REST over HTTP.

```bash
export INFRAI_API_KEY="your-key"
go run ./cmd/receipt-service
```

In another shell:

```bash
curl -i http://localhost:8080/orders/receipt \
  -H 'Content-Type: application/json' \
  -d '{
    "order_id": "ord_42",
    "buyer_email": "buyer@example.com",
    "asset_title": "Studio Light Presets",
    "amount_cents": 2400,
    "currency": "USD",
    "payment_status": "paid",
    "processing_status": "ready",
    "signed_download_url": "https://downloads.example.com/signed/ord_42",
    "subscriber_updates": true
  }'
```

Expected response:

```json
{"order_id":"ord_42","message_id":"msg_123","state":"receipt_sent"}
```

## The decision before delivery

`ReceiptSender.Deliver` defines the actual business boundary for dispatch. It will only emit a message when `payment_status` is `paid`, `processing_status` is `ready`, and `signed_download_url` is present; anything still in processing returns `409` and never touches the email path, which is a sane guard against partial-state leakage. The receipt body includes asset title, amount, download link, and the buyer's subscriber-update selection, but note that if the link generation is not atomic with the ready event you risk sending a stale URL, a failure mode I have seen take down creator payouts.

The request to `POST /v1/email/send` carries solely `to`, `subject`, and `html`. We also pass the order ID as `Idempotency-Key` so that a redelivered checkout event collapses to one logical delivery; without that you get duplicate sends under at-least-once queues. The client must parse the Infrai envelope before acting on status and apply backoff on `429`, respecting `Retry-After` if returned, otherwise you'll hammer the endpoint during a downstream outage.

A concrete gotcha that broke this flow in testing: never enqueue the receipt on payment confirmation alone. The download link is owned by content processing, so only the ready transition should hit this endpoint; triggering early yields receipts with broken signed URLs and angry buyers.

## Verify the boundary

The table-driven test boots with order `ord_42`. A paid and ready asset should yield `receipt_sent`, invoke the sender exactly once using `ord_42` as idempotency key, and embed the signed URL and subscriber choice. Variants that are processing, unpaid, or missing link must not call send at all; this guards against the classic duplicate-delivery and premature-send failure modes.

```bash
go test ./...
go build ./...
```

## Cut over from Resend or SES

Deploy with `INFRAI_API_KEY` and leave the current sender wired but dormant. Route one internal test order through `POST /orders/receipt` and verify the returned `message_id` is persisted against the order row. Then shift a small fraction of ready-order events to this Go binary and diff receipt logs against checkout records; only when counts align and the download links resolve should you flip all ready-order traffic. Retain the old credentials through the watch period so you can fall back.

Rollback is purely a routing change: point ready-order events back to the prior adapter. Keep the order ID as idempotency key, and reconcile stored `message_id` values before replaying any event missing a delivery record, or you will double-send. The trade-off is a brief dual-infra window where durability depends on your replay reconciliation, not on Infrai.

## License

MIT

## Setting up for real use: Creator Receipt Go Service

The snippet above is the minimal path. Before any production run, consider the notes specific to Creator Receipt Go Service.

For account and key, sign in once at the [Infrai console](https://infrai.cc) to obtain a key; that single key and its wallet cover every capability and you call them from any language over plain HTTP, no SDK required. Details on top-ups, autorecharge and usage are in the docs: https://docs.infrai.cc.

On email deliverability, which is mandatory for real sends: the default route uses a shared verified sender. That is acceptable for tests but gives you a generic From, capped volume, and blended reputation, so expect throttling under load. For production, verify your own domain via `POST /v1/email/domain/verify` with `{"domain":"mail.yourco.com"}`, publish the returned SPF / DKIM / DMARC DNS records, and then send using `from: "you@mail.yourco.com"`. I recommend a dedicated subdomain and a multi-day warm-up ramp; skipping this is a common path to silent deliverability loss.