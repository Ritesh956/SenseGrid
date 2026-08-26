"use client";

import { Suspense, useState, type FormEvent } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { signIn } from "next-auth/react";

function LoginForm() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);

    const result = await signIn("credentials", {
      username,
      password,
      redirect: false,
    });

    setSubmitting(false);
    if (result?.error) {
      setError("Invalid username or password.");
      return;
    }
    router.push(searchParams.get("from") ?? "/");
    router.refresh();
  }

  return (
    <form
      onSubmit={onSubmit}
      className="w-full max-w-sm rounded-lg border border-border bg-surface p-8 shadow-lg"
    >
      <div className="mb-6 flex items-baseline gap-2">
        <span className="h-2.5 w-2.5 rounded-full bg-accent" />
        <h1 className="text-lg font-semibold tracking-wide text-text">SenseGrid Console</h1>
      </div>

      <label className="mb-1 block text-sm text-text-muted" htmlFor="username">
        Username
      </label>
      <input
        id="username"
        className="mb-4 w-full rounded border border-border bg-surface-2 px-3 py-2 text-text outline-none focus:border-accent"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        autoComplete="username"
        required
      />

      <label className="mb-1 block text-sm text-text-muted" htmlFor="password">
        Password
      </label>
      <input
        id="password"
        type="password"
        className="mb-6 w-full rounded border border-border bg-surface-2 px-3 py-2 text-text outline-none focus:border-accent"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        autoComplete="current-password"
        required
      />

      {error && <p className="mb-4 text-sm text-crit">{error}</p>}

      <button
        type="submit"
        disabled={submitting}
        className="w-full rounded bg-accent px-3 py-2 font-medium text-bg disabled:opacity-60"
      >
        {submitting ? "Signing in…" : "Sign in"}
      </button>
    </form>
  );
}

export default function LoginPage() {
  return (
    <main className="flex min-h-screen items-center justify-center px-4">
      {/* useSearchParams (for a post-login redirect target) requires a
          Suspense boundary for this page to be statically prerendered. */}
      <Suspense fallback={null}>
        <LoginForm />
      </Suspense>
    </main>
  );
}
