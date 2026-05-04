"use client";

export function normalizeImageUrl(src: string) {
  if (!src || typeof window === "undefined" || !src.startsWith("http://")) {
    return src;
  }

  try {
    const url = new URL(src);
    if (window.location.protocol === "https:" && url.host === window.location.host) {
      url.protocol = "https:";
      return url.toString();
    }
  } catch {
    return src;
  }

  return src;
}
