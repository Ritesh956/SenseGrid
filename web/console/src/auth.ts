import NextAuth from "next-auth";
import Credentials from "next-auth/providers/credentials";

// The console's only identity provider: POST /v1/auth/login on
// cmd/control (cmd/control/auth_login.go), backed by internal/users. There
// is no separate identity store here — NextAuth's job is entirely to hold
// the control-plane's own JWT in a session, not to mint its own.
const CONTROL_API_URL = process.env.CONTROL_API_URL ?? "https://control:8080";

// next-auth@5's beta module augmentation (src/types/next-auth.d.ts) covers
// consumers of Session — pages reading session.user.role,
// session.accessToken. It doesn't reliably merge into the callback
// parameter types below (a known beta rough edge), so the callbacks cast
// through this local shape instead of fighting the package's typings.
interface AppTokenFields {
  role?: string;
  accessToken?: string;
  accessTokenExpires?: string;
  error?: string;
}

export const { handlers, signIn, signOut, auth } = NextAuth({
  session: { strategy: "jwt" },
  pages: { signIn: "/login" },
  // Auth.js only trusts the incoming request's Host header automatically
  // on Vercel; everywhere else (this console's actual deployment target —
  // deploy/docker-compose.yml's plain Docker container) it refuses every
  // request as "UntrustedHost" unless told to trust it explicitly. Found
  // live: without this, every request 307-redirected in a loop between /
  // and /login with no useful error surfaced to the browser, only
  // "[auth][error] UntrustedHost" in the container's own logs.
  trustHost: true,
  providers: [
    Credentials({
      credentials: {
        username: { label: "Username", type: "text" },
        password: { label: "Password", type: "password" },
      },
      authorize: async (credentials) => {
        const username = credentials?.username;
        const password = credentials?.password;
        if (typeof username !== "string" || typeof password !== "string") {
          return null;
        }

        const res = await fetch(`${CONTROL_API_URL}/v1/auth/login`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ username, password }),
        });
        if (!res.ok) {
          return null;
        }
        const body = (await res.json()) as {
          access_token: string;
          role: string;
          username: string;
          expires_at: string;
        };
        return {
          id: body.username,
          name: body.username,
          role: body.role,
          accessToken: body.access_token,
          accessTokenExpires: body.expires_at,
        };
      },
    }),
  ],
  callbacks: {
    async jwt({ token, user }) {
      const t = token as typeof token & AppTokenFields;
      if (user) {
        const u = user as typeof user & AppTokenFields;
        t.role = u.role;
        t.accessToken = u.accessToken;
        t.accessTokenExpires = u.accessTokenExpires;
        t.error = undefined;
      }
      // No refresh-token flow — cmd/control's auth model (see auth.go)
      // issues a single access token, nothing to exchange it for. Once it
      // expires, mark the session so client code can redirect to /login
      // rather than keep using a token every REST/WS call will now reject.
      if (typeof t.accessTokenExpires === "string" && Date.now() > Date.parse(t.accessTokenExpires)) {
        t.error = "TokenExpired";
      }
      return t;
    },
    async session({ session, token }) {
      const t = token as typeof token & AppTokenFields;
      session.user.role = t.role;
      session.accessToken = t.accessToken;
      session.error = t.error;
      return session;
    },
  },
});
