# Fawtara Platform — Architecture

## 1. Overview

Fawtara is a bill aggregation hub (EBPP). It sits between **billers** (utilities,
telecom, government entities) and **payment channels** (banks, wallets, agent apps).
A customer inquires about a bill from any channel, pays it there, and the platform:

1. validates and routes the payment,
2. records it atomically in the **Formance Ledger** (double-entry, multi-posting),
3. notifies the biller,
4. settles collected funds to billers on a daily cycle, net of fees,
5. reconciles ledger state against bank statements and biller records.

## 2. Money model

All money movements are ledger transactions written in Numscript. The ledger is the
single source of truth; every other system state (settlement batches, recon reports)
is derived from it.

### Payment flow (happy path)

The channel collected cash from the customer; the platform records the
channel's obligation (its `due` account goes negative — a receivable) and
splits the gross across the biller, the fee, and the channel commission:

```
channels:<channel>:due (overdraft) ──▶ billers:<biller>:payable    (remaining)
                                   ──▶ platform:fees:revenue       (fee)
                                   ──▶ channels:<channel>:commission (rebate)
```

See `numscripts/bill_payment.num`.

### Clearing & settlement flow (end of day)

Mirroring the CBJ eFAWATEERcom regulatory framework, **the platform does not
move money**. At cut-off, `cmd/settlement`:

1. issues the **NCP (net clearing position) statement** — channel settlement
   banks as debits, biller settlement banks as credits — plus a separate
   **commissions statement**, both submitted to the central bank's RTGS,
2. the RTGS executes the settlement across the settlement banks' accounts at
   the central bank,
3. the ledger records the clearing postings, tagged with the batch id:

```
billers:<biller>:payable ──▶ billers:<biller>:settled   (credit side)
world ──▶ channels:<channel>:due                        (debit side, back to zero)
```

The NCP files and the ledger postings share a `settlement_batch_id` so recon
can match them against the RTGS settlement result.

### Refund flow

Reverse posting from `billers:<biller>:payable` (if unsettled) or
`platform:refunds:clearing` (if already settled) back to the channel.
See `numscripts/refund.num`.

## 3. Services

### channel-api (port 8081)
Public-facing API consumed by banks/wallets/apps.
- `POST /v1/bills/inquiry` — look up amount due for a subscriber at a biller
- `POST /v1/payments` — pay a bill; writes the ledger transaction and notifies the biller
- `GET  /v1/payments/{id}` — payment status
- Per-channel API keys, request signing, rate limits (TODO).

### biller-gateway (port 8082)
Internal service owning one adapter per biller. Adapter interface:
`Inquire`, `NotifyPayment`, `Cancel`. Ships with a `mock` adapter for development.
Real adapters (ISO 8583 / SOAP / REST, per biller spec) plug in behind the same
interface.

### settlement (cron / port 8083)
- Aggregates channel `due` and biller `payable` balances at cut-off,
- issues the NCP payments statement and the commissions statement (CSV files
  under `FAWTARA_SETTLE_OUT`) for submission to the central bank's RTGS,
- records the clearing postings in the ledger, tagged with the batch id.

### recon (cron / port 8084)
- Pulls ledger balances & transaction logs,
- pulls external sources (bank statements, biller confirmation files),
- computes drift per asset/account, produces a report of mismatches.

## 4. Infrastructure

Formance components are consumed as pinned Docker images (MIT community edition
only — no `ee/` components):

- `ghcr.io/formancehq/ledger` — core ledger (Postgres-backed)
- Postgres 16

Production deployment targets Kubernetes with the Formance operator for the ledger,
and plain Deployments for platform services.

## 5. Non-functional requirements

- **Idempotency**: every payment carries a channel-supplied idempotency key; the
  ledger's transaction reference enforces uniqueness.
- **Audit**: the ledger log is append-only and hash-chained — regulatory audit trail
  comes for free.
- **Availability**: channel-api and biller-gateway are stateless and scale
  horizontally; the ledger is the only stateful core.
- **Compliance**: platform operation as a national/licensed bill-payment hub requires
  the relevant central-bank licensing (e.g. PSP license in Jordan). This codebase is
  the technical foundation only.
