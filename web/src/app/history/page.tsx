"use client";

import { useEffect, useRef, useState } from "react";
import { Download, Images, LoaderCircle, Sparkles } from "lucide-react";
import { toast } from "sonner";

import { ImageLightbox } from "@/components/image-lightbox";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  fetchImageHistory,
  fetchImageHistoryImage,
  type ApiHistoryImage,
  type ApiImageHistoryRecord,
} from "@/lib/api";

const PAGE_SIZE = 24;

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "未知时间";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function formatTokenCount(value: number) {
  return new Intl.NumberFormat("zh-CN").format(Math.max(0, value || 0));
}

function buildCacheKey(recordId: string, imageId: string) {
  return `${recordId}:${imageId}`;
}

export default function HistoryPage() {
  const objectUrlRef = useRef<Record<string, string>>({});
  const [records, setRecords] = useState<ApiImageHistoryRecord[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [thumbnailUrls, setThumbnailUrls] = useState<Record<string, string>>({});
  const [lightboxImages, setLightboxImages] = useState<Array<{ id: string; src: string }>>([]);
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [lightboxIndex, setLightboxIndex] = useState(0);
  const [loadingRecordId, setLoadingRecordId] = useState<string | null>(null);
  const [page, setPage] = useState(1);

  const totalPages = Math.max(1, Math.ceil(records.length / PAGE_SIZE));
  const safePage = Math.min(page, totalPages);
  const pageRecords = records.slice((safePage - 1) * PAGE_SIZE, safePage * PAGE_SIZE);
  const totalImages = records.reduce((sum, record) => sum + Math.max(0, record.image_count), 0);
  const totalOutputTokens = records.reduce((sum, record) => sum + Math.max(0, record.usage?.output_tokens || 0), 0);

  useEffect(() => {
    let cancelled = false;

    const loadHistory = async () => {
      try {
        const data = await fetchImageHistory();
        if (cancelled) {
          return;
        }
        setRecords(data.items || []);
      } catch (error) {
        const message = error instanceof Error ? error.message : "读取 API 历史失败";
        toast.error(message);
      } finally {
        if (!cancelled) {
          setIsLoading(false);
        }
      }
    };

    void loadHistory();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (page > totalPages) {
      setPage(totalPages);
    }
  }, [page, totalPages]);

  useEffect(() => {
    let cancelled = false;

    const loadThumbnails = async () => {
      for (const record of pageRecords) {
        const firstImage = record.images?.[0];
        if (!firstImage) {
          continue;
        }
        const cacheKey = buildCacheKey(record.id, firstImage.id);
        if (objectUrlRef.current[cacheKey]) {
          continue;
        }
        try {
          const blob = await fetchImageHistoryImage(record.id, firstImage.id);
          if (cancelled) {
            return;
          }
          const objectUrl = URL.createObjectURL(blob);
          objectUrlRef.current[cacheKey] = objectUrl;
          setThumbnailUrls((current) => ({ ...current, [cacheKey]: objectUrl }));
        } catch (error) {
          const message = error instanceof Error ? error.message : "加载缩略图失败";
          toast.error(message);
          return;
        }
      }
    };

    void loadThumbnails();
    return () => {
      cancelled = true;
    };
  }, [pageRecords]);

  useEffect(() => {
    return () => {
      Object.values(objectUrlRef.current).forEach((url) => URL.revokeObjectURL(url));
    };
  }, []);

  const openRecordLightbox = async (record: ApiImageHistoryRecord, startIndex = 0) => {
    if (!record.images?.length) {
      return;
    }

    setLoadingRecordId(record.id);
    try {
      const nextImages: Array<{ id: string; src: string }> = [];
      for (const image of record.images) {
        const cacheKey = buildCacheKey(record.id, image.id);
        let src = objectUrlRef.current[cacheKey];
        if (!src) {
          const blob = await fetchImageHistoryImage(record.id, image.id);
          src = URL.createObjectURL(blob);
          objectUrlRef.current[cacheKey] = src;
          setThumbnailUrls((current) => ({ ...current, [cacheKey]: src }));
        }
        nextImages.push({ id: image.id, src });
      }
      setLightboxImages(nextImages);
      setLightboxIndex(startIndex);
      setLightboxOpen(true);
    } catch (error) {
      const message = error instanceof Error ? error.message : "加载大图失败";
      toast.error(message);
    } finally {
      setLoadingRecordId(null);
    }
  };

  const handleDownload = async (recordId: string, image: ApiHistoryImage) => {
    try {
      const blob = await fetchImageHistoryImage(recordId, image.id);
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = objectUrl;
      link.download = image.file_name || `${image.id}.png`;
      link.click();
      URL.revokeObjectURL(objectUrl);
    } catch (error) {
      const message = error instanceof Error ? error.message : "下载图片失败";
      toast.error(message);
    }
  };

  return (
    <>
      <section className="mx-auto flex w-full max-w-[1380px] flex-col gap-4 pb-6">
        <Card className="overflow-hidden border-stone-200/80 bg-white/80">
          <CardHeader className="pb-4">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <CardTitle className="text-xl text-stone-950">API 图片历史</CardTitle>
                <CardDescription className="mt-2 text-stone-500">
                  查看通过 API 生成并落盘保存的图片记录。当前仅展示服务端统一历史，不包含浏览器本地画图记录。
                </CardDescription>
              </div>
              <div className="flex flex-wrap gap-3 text-sm text-stone-600">
                <span className="rounded-full bg-stone-100 px-4 py-2">记录 {formatTokenCount(records.length)}</span>
                <span className="rounded-full bg-stone-100 px-4 py-2">图片 {formatTokenCount(totalImages)}</span>
                <span className="rounded-full bg-stone-100 px-4 py-2">
                  输出 Token {formatTokenCount(totalOutputTokens)}
                </span>
              </div>
            </div>
          </CardHeader>
        </Card>

        {isLoading ? (
          <Card className="border-dashed border-stone-200/80 bg-white/75">
            <CardContent className="flex min-h-64 items-center justify-center gap-3 py-16 text-stone-500">
              <LoaderCircle className="size-5 animate-spin" />
              <span>正在读取 API 历史...</span>
            </CardContent>
          </Card>
        ) : pageRecords.length === 0 ? (
          <Card className="border-dashed border-stone-200/80 bg-white/75">
            <CardContent className="flex min-h-64 flex-col items-center justify-center gap-4 py-16 text-center text-stone-500">
              <div className="rounded-full bg-stone-100 p-4">
                <Sparkles className="size-5" />
              </div>
              <div>
                <p className="text-base font-medium text-stone-700">还没有 API 图片历史</p>
                <p className="mt-1 text-sm">等外部客户端通过图片接口生成成功后，这里会自动出现可回看的记录。</p>
              </div>
            </CardContent>
          </Card>
        ) : (
          <>
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
              {pageRecords.map((record) => {
                const firstImage = record.images?.[0];
                const cacheKey = firstImage ? buildCacheKey(record.id, firstImage.id) : "";
                const previewUrl = cacheKey ? thumbnailUrls[cacheKey] : "";
                const isOpening = loadingRecordId === record.id;

                return (
                  <Card key={record.id} className="overflow-hidden border-stone-200/80 bg-white/88">
                    <button
                      type="button"
                      onClick={() => void openRecordLightbox(record)}
                      className="group relative flex aspect-[4/3] w-full items-center justify-center overflow-hidden bg-stone-100"
                    >
                      {previewUrl ? (
                        <img
                          src={previewUrl}
                          alt={record.prompt || "API 生成图片"}
                          className="h-full w-full object-cover transition duration-300 group-hover:scale-[1.03]"
                        />
                      ) : (
                        <div className="flex items-center gap-2 text-sm text-stone-500">
                          <LoaderCircle className="size-4 animate-spin" />
                          <span>加载预览中</span>
                        </div>
                      )}
                      <div className="absolute inset-x-0 bottom-0 flex items-center justify-between bg-gradient-to-t from-black/70 via-black/25 to-transparent px-4 py-3 text-xs text-white">
                        <span>{record.model}</span>
                        <span>{record.image_count} 张</span>
                      </div>
                    </button>
                    <CardContent className="space-y-4 p-5">
                      <div className="space-y-2">
                        <div className="flex flex-wrap gap-2 text-xs">
                          <span className="rounded-full bg-stone-100 px-3 py-1 text-stone-600">
                            {record.mode === "edit" ? "编辑" : "生成"}
                          </span>
                          <span className="rounded-full bg-stone-100 px-3 py-1 text-stone-600">
                            {record.source_endpoint}
                          </span>
                          <span className="rounded-full bg-stone-100 px-3 py-1 text-stone-600">
                            {formatDateTime(record.created_at)}
                          </span>
                        </div>
                        <p className="line-clamp-3 text-sm leading-6 text-stone-700">
                          {record.prompt || "无提示词"}
                        </p>
                      </div>
                      <div className="flex items-center justify-between rounded-2xl bg-stone-50 px-4 py-3 text-xs text-stone-600">
                        <div className="flex items-center gap-2">
                          <Images className="size-4" />
                          <span>输入 {formatTokenCount(record.usage?.input_tokens || 0)}</span>
                        </div>
                        <span>输出 {formatTokenCount(record.usage?.output_tokens || 0)}</span>
                        <span>总计 {formatTokenCount(record.usage?.total_tokens || 0)}</span>
                      </div>
                      <div className="flex items-center gap-3">
                        <Button
                          type="button"
                          variant="outline"
                          className="flex-1 rounded-2xl border-stone-200 bg-white"
                          onClick={() => void openRecordLightbox(record)}
                          disabled={isOpening}
                        >
                          {isOpening ? <LoaderCircle className="size-4 animate-spin" /> : null}
                          查看图片
                        </Button>
                        {firstImage ? (
                          <Button
                            type="button"
                            variant="ghost"
                            className="rounded-2xl"
                            onClick={() => void handleDownload(record.id, firstImage)}
                          >
                            <Download className="size-4" />
                            下载首图
                          </Button>
                        ) : null}
                      </div>
                    </CardContent>
                  </Card>
                );
              })}
            </div>

            <div className="flex items-center justify-between gap-3 rounded-3xl border border-white/70 bg-white/70 px-5 py-4">
              <p className="text-sm text-stone-500">
                第 {safePage} / {totalPages} 页，共 {formatTokenCount(records.length)} 条记录
              </p>
              <div className="flex gap-3">
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-2xl border-stone-200 bg-white"
                  disabled={safePage <= 1}
                  onClick={() => setPage((current) => Math.max(1, current - 1))}
                >
                  上一页
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  className="rounded-2xl border-stone-200 bg-white"
                  disabled={safePage >= totalPages}
                  onClick={() => setPage((current) => Math.min(totalPages, current + 1))}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </section>

      <ImageLightbox
        images={lightboxImages}
        currentIndex={lightboxIndex}
        open={lightboxOpen}
        onOpenChange={setLightboxOpen}
        onIndexChange={setLightboxIndex}
      />
    </>
  );
}
