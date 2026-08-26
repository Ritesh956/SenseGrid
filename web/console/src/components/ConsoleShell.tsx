"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { signOut } from "next-auth/react";
import { SocketProvider } from "./SocketProvider";
import ConnectionBadge from "./ConnectionBadge";

const NAV = [
  { href: "/", label: "Fleet" },
  { href: "/alerts", label: "Alerts" },
  { href: "/rollouts", label: "Rollouts" },
];

export default function ConsoleShell({
  username,
  role,
  accessToken,
  children,
}: {
  username: string;
  role: string;
  accessToken?: string;
  children: React.ReactNode;
}) {
  const pathname = usePathname();

  return (
    <SocketProvider accessToken={accessToken} role={role}>
      <div className="flex min-h-screen">
        <aside className="w-56 shrink-0 border-r border-border p-4">
          <div className="mb-8 flex items-baseline gap-2 px-2">
            <span className="h-2.5 w-2.5 rounded-full bg-accent" />
            <span className="text-sm font-semibold tracking-wide">SenseGrid</span>
          </div>
          <nav className="flex flex-col gap-1">
            {NAV.map((item) => {
              const active = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`rounded px-3 py-2 text-sm ${
                    active ? "bg-surface-2 text-text" : "text-text-muted hover:bg-surface-2 hover:text-text"
                  }`}
                >
                  {item.label}
                </Link>
              );
            })}
          </nav>
        </aside>

        <div className="flex flex-1 flex-col">
          <header className="flex items-center justify-between border-b border-border px-6 py-3">
            <ConnectionBadge />
            <div className="flex items-center gap-3 text-sm">
              <span className="text-text-muted">
                {username} <span className="text-xs uppercase">· {role}</span>
              </span>
              <button
                onClick={() => signOut({ callbackUrl: "/login" })}
                className="rounded border border-border px-2 py-1 text-xs text-text-muted hover:text-text"
              >
                Sign out
              </button>
            </div>
          </header>
          <main className="flex-1 overflow-y-auto p-6">{children}</main>
        </div>
      </div>
    </SocketProvider>
  );
}
