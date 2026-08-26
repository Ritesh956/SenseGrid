import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "SenseGrid Console",
  description: "Operate the SenseGrid fleet — devices, alerts, rollouts.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased">{children}</body>
    </html>
  );
}
