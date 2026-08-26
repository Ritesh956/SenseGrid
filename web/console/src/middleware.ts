import { NextResponse } from "next/server";
import { auth } from "@/auth";

// Route protection for every page except /login itself and the NextAuth
// API routes it depends on to log in in the first place.
export default auth((req) => {
  const isLoggedIn = !!req.auth && !req.auth.error;
  const isLoginPage = req.nextUrl.pathname.startsWith("/login");

  if (!isLoggedIn && !isLoginPage) {
    const url = new URL("/login", req.nextUrl.origin);
    url.searchParams.set("from", req.nextUrl.pathname);
    return NextResponse.redirect(url);
  }
  if (isLoggedIn && isLoginPage) {
    return NextResponse.redirect(new URL("/", req.nextUrl.origin));
  }
  return NextResponse.next();
});

export const config = {
  matcher: ["/((?!api/auth|_next/static|_next/image|favicon.ico).*)"],
};
