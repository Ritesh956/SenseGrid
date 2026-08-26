const BASE_DELAY_MS = 1000;
const MAX_DELAY_MS = 30000;

// backoffDelayMs computes the reconnect delay for the Nth consecutive
// close (0-indexed) — doubling from 1s up to a 30s cap, so a console left
// open through a longer control-plane outage doesn't hammer it with
// reconnect attempts, while a quick blip still reconnects fast.
export function backoffDelayMs(attempt: number): number {
  return Math.min(BASE_DELAY_MS * 2 ** attempt, MAX_DELAY_MS);
}
