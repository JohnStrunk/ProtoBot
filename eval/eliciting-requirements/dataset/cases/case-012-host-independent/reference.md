# Reference properties

- Preserve `BILL-REQ-42`, `billing`, `external-api`, and the interface trace.
- Select the complex pattern because the valid-input precondition and receipt
  event jointly scope the obligation; keep the API as the named system.
- Preserve valid amount/currency, the 500 ms service-boundary measurement, and
  all three decision values.
- Treat the host metadata as metadata, not as a reason to invent a lifecycle.
- Do not claim approval or use ProtoBot-specific terminology.
