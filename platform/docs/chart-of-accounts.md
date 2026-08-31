# Chart of Accounts

All accounts live in one ledger named `fawtara`. Amounts use asset `JOD/3`
(millimes precision) unless stated otherwise.

The platform never holds or moves money itself (mirroring the CBJ eFAWATEERcom
framework): channels collect from customers, the central bank's RTGS settles
net positions between settlement banks from the NCP statement we issue. The
ledger tracks obligations, not cash.

| Account | Meaning |
|---|---|
| `world` | External world (RTGS settlement executed at the central bank) |
| `channels:<channel_id>:due` | Receivable from the channel's settlement bank (negative balance = amount owed to the system for bills it collected) |
| `channels:<channel_id>:commission` | Commission/rebate earned by the channel |
| `billers:<biller_id>:payable` | Collected amounts owed to a biller, not yet included in an NCP batch |
| `billers:<biller_id>:settled` | Amounts already cleared through an NCP batch (cumulative) |
| `platform:fees:revenue` | Platform's own fee revenue |
| `platform:refunds:clearing` | Clearing account for refunds of already-settled payments |

## Invariants

- `channels:<id>:due` (negated) at cut-off equals that channel's **debit** line
  in the NCP statement; it returns to zero when the batch's clearing posting
  records the RTGS execution.
- `billers:<id>:payable` at cut-off equals that biller's **credit** line in the
  NCP statement; it drops to zero after the clearing posting.
- Sum of NCP debits = sum of NCP credits + commissions settled that day
  (double-entry guarantees it).
- Every payment transaction carries metadata: `payment_id`, `biller_id`,
  `channel_id`, `subscriber_ref`, `bill_ref`.
- Every clearing transaction carries `settlement_batch_id` and `cutoff_date`,
  matching the issued NCP statement files.
