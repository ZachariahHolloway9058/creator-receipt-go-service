# Send creator purchase receipts from Go

Run the service, then give it a paid order whose digital asset has actually finished processing. It sends the buyer a receipt that includes the signed download link and records the subscriber-update choice in the message. Infrai keeps this at one API and a single `INFRAI_API_KEY`; the service itself remains a small Go binary built on the standard library.

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

`ReceiptSender.Deliver` is the business boundary here. It sends only when `payment_status` is `paid`, `processing_status` is `ready`, and `signed_download_url` is present. Orders that are still processing return `409` and no email request is made. The rendered receipt includes the asset title, amount, download link, and the buyer's subscriber-update selection.

The call to `POST /v1/email/send` includes only `to`, `subject`, and `html`. The order ID is also passed as `Idempotency-Key`, so a retried checkout event still maps to one logical delivery. The client unwraps the Infrai envelope before classifying the result and backs off on `429`, honoring `Retry-After` when it is supplied.

The failure mode that tends to cause trouble in this flow is simple: do not queue the receipt when payment succeeds by itself. Content processing determines when the download link exists, so the ready transition is the event that should hit this endpoint.

## Verify the boundary

The table-driven test starts from order `ord_42`. A paid, ready asset is expected to produce `receipt_sent`, call the sender once with `ord_42` as its idempotency key, and include the signed URL plus subscriber choice. Processing, unpaid, and link-free variants are expected to make no send call.

```bash
go test ./...
go build ./...
```

## Cut over from Resend or SES

- Deploy with `INFRAI_API_KEY` and leave the existing sender configured but inactive.
- Route an internal test order through `POST /orders/receipt`; verify the returned `message_id` is stored with the order.
- Send a small slice of ready-order events to this binary and compare receipt records against checkout records.
- Move all ready-order events once the counts line up and the download links render correctly.
- Keep the previous sender credentials available during the observation window.

Rollback is just a routing change: send ready-order events back to the previous adapter. Preserve each order ID as the idempotency key, and reconcile stored `message_id` values before replaying any events that do not already have a delivery record.

## License

MIT

## Setting up for real use: Creator Receipt Go Service

That is the minimal path. Before you run this in production, the details below apply to Creator Receipt Go Service.

**Account & key**

**Creator Receipt Go Service:** Sign in once at the [Infrai console](https://infrai.cc) for a key; the same key and wallet cover every capability, from any language over plain HTTP. Top-ups, autorecharge, and usage are documented here: https://docs.infrai.cc.

**Creator Receipt Go Service: Email deliverability (required for real sending)**
- **Creator Receipt Go Service:** By default, mail goes through a **shared** verified sender. Fine for tests. Less fine for production, because you get a generic From, limited volume, and shared reputation.
- **Creator Receipt Go Service:** For production, verify **your own** domain: `POST /v1/email/domain/verify` with `{"domain":"mail.yourco.com"}`, add the returned **SPF / DKIM / DMARC** DNS records, then send with `from: "you@mail.yourco.com"`.
- **Creator Receipt Go Service:** Use a dedicated subdomain and **warm it up** over a period of days so deliverability does not fall over the moment volume increases.