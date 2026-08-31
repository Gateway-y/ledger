# Chart of Accounts

All accounts live in one ledger named `fawtara`. Amounts use asset `JOD/3`
(millimes precision) unless stated otherwise.

| Account | Meaning |
|---|---|
| `world` | External world (customer funds entering/leaving the system) |
| `channels:<channel_id>:collections` | Funds a channel has collected from customers, owed to the platform |
| `channels:<channel_id>:commission` | Commission/rebate earned by the channel |
| `billers:<biller_id>:payable` | Collected funds owed to a biller, not yet settled |
| `billers:<biller_id>:settled` | Funds already paid out to the biller (cumulative) |
| `platform:fees:revenue` | Platform's own fee revenue |
| `platform:refunds:clearing` | Clearing account for refunds of already-settled payments |

## Invariants

- `channels:*:collections` balance equals what channels owe us; recon compares it
  against actual bank statement credits.
- `billers:*:payable` balance at cut-off equals the settlement payout amount for
  that biller; drops to zero after each settlement run.
- Every payment transaction carries metadata: `payment_id`, `biller_id`,
  `channel_id`, `subscriber_ref`, `idempotency_key`.
- Every settlement transaction carries `settlement_batch_id` and `cutoff_date`.
