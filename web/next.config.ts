import type { NextConfig } from "next";

// Static export: the Go relay serves web/out at the same origin as /ws.
const config: NextConfig = {
  output: "export",
  images: { unoptimized: true },
  reactStrictMode: true,
};

export default config;
