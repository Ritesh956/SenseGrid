import { redirect } from "next/navigation";
import { auth } from "@/auth";
import ConsoleShell from "@/components/ConsoleShell";

export default async function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const session = await auth();
  // Belt-and-suspenders alongside middleware.ts's route protection —
  // middleware runs on the edge runtime and matches by path, this runs
  // server-side per request; both need to agree "no session, no token
  // expiry error" means logged out.
  if (!session?.user || session.error) {
    redirect("/login");
  }

  return (
    <ConsoleShell
      username={session.user.name ?? ""}
      role={session.user.role ?? "viewer"}
      accessToken={session.accessToken}
    >
      {children}
    </ConsoleShell>
  );
}
