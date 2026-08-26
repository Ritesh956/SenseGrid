import type { DefaultSession } from "next-auth";

// Module augmentation for the extra fields src/auth.ts's jwt/session
// callbacks carry through — role and accessToken (the control-plane JWT
// itself, forwarded to cmd/control by src/lib/control-api.ts and used
// directly by the browser for the WS handshake's ?token= param), plus
// error (set when the token has expired, since the backend has no
// refresh-token flow to fall back to — see src/auth.ts).
declare module "next-auth" {
  interface Session {
    accessToken?: string;
    error?: string;
    user: {
      role?: string;
    } & DefaultSession["user"];
  }

  interface User {
    role?: string;
    accessToken?: string;
    accessTokenExpires?: string;
  }
}

declare module "next-auth/jwt" {
  interface JWT {
    role?: string;
    accessToken?: string;
    accessTokenExpires?: string;
    error?: string;
  }
}
