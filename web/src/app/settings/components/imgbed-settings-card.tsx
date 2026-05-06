"use client";

import { CloudUpload, LoaderCircle, PlugZap, Save } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { testImgbedConnection } from "@/lib/api";

import { useSettingsStore } from "../store";

export function ImgbedSettingsCard() {
  const [isTestingConnection, setIsTestingConnection] = useState(false);
  const config = useSettingsStore((state) => state.config);
  const isLoadingConfig = useSettingsStore((state) => state.isLoadingConfig);
  const isSavingConfig = useSettingsStore((state) => state.isSavingConfig);
  const setImgbedField = useSettingsStore((state) => state.setImgbedField);
  const saveConfig = useSettingsStore((state) => state.saveConfig);

  const imgbed = config?.imgbed;

  if (isLoadingConfig || !imgbed) {
    return (
      <Card className="rounded-2xl border-stone-200/70 bg-white/95 shadow-sm">
        <CardContent className="flex items-center gap-3 p-6 text-sm text-stone-500">
          <LoaderCircle className="size-4 animate-spin" />
          加载图床配置...
        </CardContent>
      </Card>
    );
  }

  const handleTestConnection = async () => {
    const baseUrl = String(imgbed.base_url || "").trim();
    if (!baseUrl) {
      toast.error("请先填写图床地址");
      return;
    }
    setIsTestingConnection(true);
    try {
      const data = await testImgbedConnection({
        base_url: baseUrl,
        api_token: String(imgbed.api_token || ""),
        folder_prefix: String(imgbed.folder_prefix || "").trim(),
        timeout_seconds: Number(imgbed.timeout_seconds) || undefined,
      });
      toast.success(`测试成功，已上传一张测试图：${data.url}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "测试失败";
      toast.error(message);
    } finally {
      setIsTestingConnection(false);
    }
  };

  const handleSave = async () => {
    await saveConfig();
  };

  return (
    <Card className="rounded-2xl border-stone-200/70 bg-white/95 shadow-sm">
      <CardContent className="space-y-5 p-6">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2 text-base font-semibold tracking-tight text-stone-900">
            <CloudUpload className="size-4 text-stone-700" />
            图床存储
          </div>
          <p className="text-xs leading-5 text-stone-500">
            将生成的图片上传至 CloudFlare-ImgBed 兼容图床。上传失败可回退本地存储，避免主流程受影响。
          </p>
        </div>

        <div className="flex items-center gap-2 rounded-xl border border-stone-200/70 bg-stone-50/70 p-3">
          <Checkbox
            id="imgbed-enabled"
            checked={Boolean(imgbed.enabled)}
            onCheckedChange={(checked) => setImgbedField("enabled", Boolean(checked))}
          />
          <label htmlFor="imgbed-enabled" className="text-sm font-medium text-stone-700">
            启用图床（关闭后图片仍走本地存储）
          </label>
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">图床地址</label>
            <Input
              value={String(imgbed.base_url || "")}
              placeholder="https://your-imgbed.example.com"
              onChange={(event) => setImgbedField("base_url", event.target.value)}
              className="h-10 rounded-xl border-stone-200 bg-white"
            />
            <p className="text-xs text-stone-500">CloudFlare-ImgBed 部署地址，不带尾部斜杠。</p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">API Token</label>
            <Input
              type="password"
              value={String(imgbed.api_token || "")}
              placeholder="在图床管理后台生成的 Token"
              onChange={(event) => setImgbedField("api_token", event.target.value)}
              className="h-10 rounded-xl border-stone-200 bg-white"
            />
            <p className="text-xs text-stone-500">需要 upload 权限。保存后接口仅返回 ******** 占位符。</p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">目录前缀</label>
            <Input
              value={String(imgbed.folder_prefix || "")}
              placeholder="chatgpt2api"
              onChange={(event) => setImgbedField("folder_prefix", event.target.value)}
              className="h-10 rounded-xl border-stone-200 bg-white"
            />
            <p className="text-xs text-stone-500">最终路径为 <code>{`${imgbed.folder_prefix || "chatgpt2api"}/年/月/日/`}</code>。</p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">上传超时（秒）</label>
            <Input
              type="number"
              min={1}
              value={String(imgbed.timeout_seconds || 30)}
              onChange={(event) => setImgbedField("timeout_seconds", Number(event.target.value) || 30)}
              className="h-10 rounded-xl border-stone-200 bg-white"
            />
            <p className="text-xs text-stone-500">超过此时间未完成上传则视为失败。</p>
          </div>
        </div>

        <div className="flex items-center gap-2 rounded-xl border border-stone-200/70 bg-stone-50/70 p-3">
          <Checkbox
            id="imgbed-fallback"
            checked={Boolean(imgbed.fallback_to_local)}
            onCheckedChange={(checked) => setImgbedField("fallback_to_local", Boolean(checked))}
          />
          <label htmlFor="imgbed-fallback" className="text-sm font-medium text-stone-700">
            上传失败时回退本地存储（推荐保持开启）
          </label>
        </div>

        <div className="flex flex-wrap gap-3 pt-2">
          <Button
            variant="outline"
            className="h-10 rounded-xl border-stone-200 bg-white px-4 text-stone-700 hover:bg-stone-50"
            onClick={() => void handleTestConnection()}
            disabled={isTestingConnection}
          >
            {isTestingConnection ? <LoaderCircle className="size-4 animate-spin" /> : <PlugZap className="size-4" />}
            测试连接
          </Button>
          <Button
            className="h-10 rounded-xl bg-stone-950 px-4 text-white hover:bg-stone-800"
            onClick={() => void handleSave()}
            disabled={isSavingConfig}
          >
            {isSavingConfig ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />}
            保存配置
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
