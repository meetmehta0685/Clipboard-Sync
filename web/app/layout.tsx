import type { Metadata, Viewport } from "next";

import { RegisterSW } from "../components/register-sw";
import "./globals.css";

export const metadata: Metadata = {
  title: "ClipSync",
  description: "End-to-end encrypted clipboard sync between your devices.",
  manifest: "/manifest.webmanifest",
  icons: { icon: "/icon.svg", apple: "/icon-192.png" },
  appleWebApp: { capable: true, title: "ClipSync", statusBarStyle: "default" },
  referrer: "no-referrer",
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f2f4f5" },
    { media: "(prefers-color-scheme: dark)", color: "#0f1428" },
  ],
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        {children}
        <RegisterSW />
      </body>
    </html>
  );
}
