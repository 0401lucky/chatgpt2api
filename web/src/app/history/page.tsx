"use client";

import { useEffect } from "react";
import { LoaderCircle } from "lucide-react";
import { useRouter } from "next/navigation";

export default function HistoryPage() {
  const router = useRouter();

  useEffect(() => {
    router.replace("/image-manager");
  }, [router]);

  return (
    <div className="flex min-h-[40vh] items-center justify-center gap-2 text-sm text-stone-500">
      <LoaderCircle className="size-4 animate-spin" />
      <span>正在进入图片管理...</span>
    </div>
  );
}
