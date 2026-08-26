// Every read on the console goes through its own BFF route (src/app/api/**),
// same-origin, so a plain fetch is enough — no base URL, no auth header
// (the route handler attaches the session's bearer token server-side).
export async function fetcher<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error((body as { error?: string }).error ?? `request failed: ${res.status}`);
  }
  return res.json() as Promise<T>;
}
