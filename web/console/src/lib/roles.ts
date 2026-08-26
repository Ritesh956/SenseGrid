// Mirrors cmd/control/auth.go's roleRank/roleAllows exactly — a hierarchy
// (admin satisfies any lower requirement), not disjoint scopes. This is a
// UI convenience only (hide the ack button, hide the config editor); the
// real enforcement is always cmd/control's requireRole on the actual
// write, since every BFF route just forwards the bearer token.
const RANK: Record<string, number> = { viewer: 0, operator: 1, admin: 2 };

export function roleAtLeast(have: string | undefined, need: string): boolean {
  if (!have || !(have in RANK)) {
    return false;
  }
  return RANK[have] >= RANK[need];
}
