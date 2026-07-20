# Failure and retry behaviour

mAPI-ng is built to fail open: a problem with metrics collection must never
affect the host application. This page describes what the client actually
does in each failure mode, based on its implementation.

## No key, or an unresolvable key

If `MAPING_KEY` is absent (and no key is passed via `WithKey`), `NewRecorder`
returns a no-op recorder: every method is a safe no-op, and no background
goroutine is started at all. There is no network activity and no
aggregation overhead. This is the safety property that makes it always safe
to add the middleware to a codebase before deciding to activate it.

If a key is present but the transport cannot be constructed (an unparseable
endpoint), the same no-op recorder is returned, and a single `Warn` log line
is emitted.

## Client-side aggregation over the flush window

With a valid, resolved key, the recorder aggregates requests in memory for
one flush window (10 seconds by default) before building an upload request.
The first flush after startup is accelerated (2 seconds) so the dashboard
shows data quickly on cold start; subsequent flushes follow the configured
window.

## When the collector is unreachable or rejects the upload

Uploads happen on a background goroutine, decoupled from the flush timer, so
a slow or down collector never blocks flushing the next window or the host
request path.

- A flushed window is pushed onto an in-memory ring buffer (sized for
  roughly five minutes of windows, floor of 8 entries) rather than sent
  immediately.
- A separate retry loop drains the oldest pending item. On success it is
  removed and the next pending item (if any) is sent immediately. On failure
  it stays in the ring and the retry loop backs off exponentially, starting
  at 1 second and doubling up to a 30-second cap.
- **The client does not distinguish failure causes.** A network error, a
  timeout, and an authentication failure (invalid key, rejected by the
  server) are all treated the same way: the send fails, the item stays
  queued, and the client retries on the same backoff schedule. An
  invalid-but-present key therefore produces indefinite retries at the
  30-second ceiling rather than a distinct "stop trying" signal on the
  client side. The first such failure is logged once at `Debug`; further
  failures are suppressed to avoid log spam.

## Backpressure: `dropped_summaries`

The ring buffer is bounded. If it fills (the collector has been unreachable
long enough that pending windows exceed capacity), the **oldest** pending
upload is evicted to make room for the new one, and the number of summaries
it contained is added to a running `dropped_summaries` counter. That counter
is stamped into the next successfully sent envelope, so data loss from
backpressure is visible in what reaches the server rather than silently
absorbed. After a successful send, the counter is reduced by the amount just
reported, so each envelope reports only the gap since the last successful
upload.

## Graceful shutdown

`Recorder.Shutdown(ctx)` stops the background goroutine, flushes whatever is
currently accumulated, and then synchronously drains the ring, retrying
transient failures with a short fixed delay until either the ring is empty
or `ctx` expires. This is a best-effort attempt to ship buffered data before
the process exits; anything still pending when `ctx` expires is abandoned.
Call it after the HTTP server has stopped accepting requests, as shown in
[Quickstart](/doc/quickstart).

## Server-side guardrails

On the ingest side, the server enforces per-tenant guardrails that are
independent of the client's own behaviour: a rate limit (token bucket per
tenant), a per-tenant payload size cap, and a best-effort series-cardinality
cap that freezes new series once a tenant's cap is reached while keeping
existing series flowing. Summaries with a client timestamp outside the
server's tolerance band are dropped rather than clamped to "now". Rejections
from any of these guards are counted and returned to the client as
`rejected_summaries` on the upload response.
