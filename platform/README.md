# Fawtara Platform

**Fawtara** is a centralized Electronic Bill Presentment & Payment (EBPP) platform — an
eFAWATEERcom-style bill aggregation hub — built on top of the Formance open-source stack.

منصة فوترة — نظام مركزي لعرض وتحصيل الفواتير يربط المفوترين (كهرباء، مياه، اتصالات، جهات
حكومية...) بقنوات الدفع (بنوك، محافظ، تطبيقات)، مبني فوق منظومة Formance مفتوحة المصدر:
دفتر الأستاذ (Ledger) كنواة محاسبية، مع خدمات خاصة بالمنصة للمفوترين والقنوات والتسوية والمطابقة.

## Architecture

```
Channels (banks, wallets, apps)
        │
        ▼
┌───────────────┐     ┌────────────────┐
│  channel-api   │────▶│ biller-gateway │──▶ Billers (electricity, water, telecom, gov)
└───────┬───────┘     └────────────────┘
        │ numscript transactions
        ▼
┌────────────────────────────────────────┐
│ Formance Ledger (docker, pinned image) │  ← single source of truth for money
└───────┬────────────────────────────────┘
        │
   ┌────┴─────┐
   ▼          ▼
settlement   recon
(issues NCP +  (daily recon files;
 commissions    ledger vs RTGS
 statements     result & participant
 to the central notices)
 bank's RTGS)
```

Per the CBJ eFAWATEERcom regulatory framework, the platform **does not move
money**: it issues end-of-day net clearing position (NCP) and commissions
statements to the central bank's RTGS, which executes settlement across the
participants' settlement-bank accounts.

- **Formance modules run as infrastructure** (pinned Docker images / k8s operator) —
  they are *not* forked into this tree. Only MIT-licensed community components are used.
- **Platform services** (this directory) are our own code, talking to Formance over its APIs.

| Component | Role |
|---|---|
| `cmd/channel-api` | Public API for payment channels: bill inquiry, pay, status |
| `cmd/biller-gateway` | Adapters to each biller: inquiry, presentment, payment notification |
| `cmd/settlement` | End-of-day clearing: issues NCP + commissions statements for the RTGS, records clearing postings |
| `cmd/recon` | Reconciles ledger state vs RTGS settlement results and participant notices |
| `numscripts/` | Numscript templates: payment, refund, settlement postings |
| `deployments/` | docker-compose to run Postgres + Formance Ledger + platform services |
| `docs/` | Architecture and chart of accounts |

## Quick start

```sh
# 1. Start infrastructure (Postgres + Formance Ledger)
docker compose -f deployments/docker-compose.yml up -d

# 2. Run a platform service
go run ./cmd/channel-api

# 3. Inquire a bill (mock biller)
curl -s localhost:8081/v1/bills/inquiry \
  -d '{"biller_id":"mock-electricity","subscriber_ref":"1234"}'

# 4. Pay it
curl -s localhost:8081/v1/payments \
  -d '{"biller_id":"mock-electricity","subscriber_ref":"1234","amount":45500,"asset":"JOD/3","channel_id":"bank_x"}'
```

## Status

Early scaffold. Working: service skeletons, ledger client, mock biller adapter,
numscript templates, NCP + commissions statement generation, local
docker-compose. Next: real biller adapters, RTGS submission format/handshake,
participant reconciliation file export, channel auth & request signing.
