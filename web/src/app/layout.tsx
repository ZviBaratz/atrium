import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  maximumScale: 5,
  userScalable: true,
};

export const metadata: Metadata = {
  title: "Atrium - Manage Multiple AI Code Assistants",
  description: "A terminal app that manages multiple AI code assistants (Claude Code, Codex, Aider, etc.) in separate workspaces, allowing you to work on multiple tasks simultaneously.",
  keywords: ["atrium", "ai", "code assistant", "terminal", "tmux", "claude code", "codex", "aider"],
  authors: [{ name: "Zvi Baratz" }],
  openGraph: {
    title: "Atrium",
    description: "A terminal app that manages multiple AI code assistants in separate workspaces",
    url: "https://zvibaratz.github.io/atrium/",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: "Atrium",
    description: "A terminal app that manages multiple AI code assistants in separate workspaces",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}