"use client";

import Link from "next/link";
import { Github } from "lucide-react";
import { usePathname, useRouter } from "next/navigation";

import webConfig from "@/constants/common-env";
import { clearStoredAuthKey } from "@/store/auth";
import { cn } from "@/lib/utils";

const navItems = [
  { href: "/image", label: "画图" },
  { href: "/history", label: "API 历史" },
  { href: "/accounts", label: "号池管理" },
  { href: "/settings", label: "设置" },
];

export function TopNav() {
  const pathname = usePathname();
  const router = useRouter();

  const handleLogout = async () => {
    await clearStoredAuthKey();
    router.replace("/login");
  };

  if (pathname === "/login") {
    return null;
  }

  return (
    <header className="border-b border-stone-100/50">
      <div className="flex h-12 items-center justify-between px-3 sm:px-6">
        <div className="flex items-center gap-2 sm:gap-3">
          <Link
            href="/image"
            className="py-1 text-[14px] font-bold tracking-tight text-stone-950 transition hover:text-stone-700 sm:text-[15px]"
          >
            chatgpt2api
          </Link>
          <a
            href="https://github.com/basketikun/chatgpt2api"
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 py-1 text-sm text-stone-400 transition hover:text-stone-700"
            aria-label="GitHub repository"
          >
            <Github className="size-4" />
            <span className="hidden md:inline">GitHub</span>
          </a>
        </div>
        <div className="flex flex-1 justify-center gap-3 sm:gap-8">
          {navItems.map((item) => {
            const active = pathname === item.href;
            return (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "relative py-1 text-[13px] font-medium transition sm:text-[15px]",
                  active ? "font-semibold text-stone-950" : "text-stone-500 hover:text-stone-900",
                )}
              >
                {item.label}
                {active ? <span className="absolute inset-x-0 -bottom-[1px] h-0.5 bg-stone-950" /> : null}
              </Link>
            );
          })}
        </div>
        <div className="flex items-center justify-end gap-2 sm:gap-3">
          <span className="hidden rounded-md bg-stone-100 px-2 py-1 text-[10px] font-medium text-stone-500 sm:inline-block sm:text-[11px]">
            v{webConfig.appVersion}
          </span>
          <button
            type="button"
            className="py-1 text-xs text-stone-400 transition hover:text-stone-700 sm:text-sm"
            onClick={() => void handleLogout()}
          >
            退出
          </button>
        </div>
      </div>
    </header>
  );
}
