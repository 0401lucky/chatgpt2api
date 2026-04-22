# API 历史页批量删除图片 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 API 历史页增加“仅当前页可选”的批量图片删除能力，并在删除后正确同步记录、缓存、统计和分页。

**Architecture:** 后端在 `ImageHistoryService` 中新增按 `record_id + image_ids` 删除图片的批处理能力，再在 FastAPI 暴露一个授权删除接口。前端历史页增加批量管理模式，只允许选择当前页图片，删除成功后使用后端返回的最新 `items` 覆盖本地状态，并清理失效的缩略图缓存。

**Tech Stack:** FastAPI、Pydantic、Python 3.13、Next.js 16、React 19、TypeScript、ESLint

---

## File Map

- `services/image_history_service.py`
  - 新增批量删除图片方法
  - 负责更新记录元数据、删除磁盘文件、删空记录时移除整条记录
- `services/api.py`
  - 新增删除请求模型
  - 新增 `POST /api/image-history/delete`
- `web/src/lib/api.ts`
  - 增加删除历史图片的请求类型和 API helper
- `web/src/app/history/page.tsx`
  - 新增批量管理模式
  - 增加当前页图片选择、确认删除、删除后状态刷新和缓存回收
- `test/test_image_history_service.py`
  - 为服务层删除能力补单元测试
- `test/test_api_image_history.py`
  - 为删除接口补 API 测试

### Task 1: 为服务层批量删除写失败测试

**Files:**
- Modify: `test/test_image_history_service.py`
- Modify: `services/image_history_service.py`

- [ ] **Step 1: 写失败测试，覆盖“删部分图片”和“删空记录”两条路径**

```python
    def test_delete_images_removes_selected_images_and_updates_record(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="批量删除测试",
            image_items=[
                {"b64_json": PNG_B64, "revised_prompt": "图一"},
                {"b64_json": PNG_B64, "revised_prompt": "图二"},
            ],
            usage={"input_tokens": 1, "output_tokens": 2112, "total_tokens": 2113},
        )

        image_ids = [image["id"] for image in record["images"]]
        result = self.service.delete_images(
            [
                {
                    "record_id": record["id"],
                    "image_ids": [image_ids[0]],
                }
            ]
        )

        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 0)
        items = self.service.list_records()
        self.assertEqual(items[0]["image_count"], 1)
        self.assertEqual([image["id"] for image in items[0]["images"]], [image_ids[1]])

    def test_delete_images_removes_record_when_last_image_deleted(self) -> None:
        record = self.service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="删空记录测试",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "唯一图片"}],
            usage={"input_tokens": 1, "output_tokens": 1056, "total_tokens": 1057},
        )

        image_id = record["images"][0]["id"]
        result = self.service.delete_images(
            [
                {
                    "record_id": record["id"],
                    "image_ids": [image_id],
                }
            ]
        )

        self.assertEqual(result["deleted_images"], 1)
        self.assertEqual(result["deleted_records"], 1)
        self.assertEqual(self.service.list_records(), [])
        self.assertIsNone(self.service.get_image_path(record["id"], image_id))
```

- [ ] **Step 2: 运行测试，确认现状下失败**

Run: `uv run pytest test/test_image_history_service.py -q`

Expected: FAIL，报错类似：

```text
AttributeError: 'ImageHistoryService' object has no attribute 'delete_images'
```

- [ ] **Step 3: 在服务层实现最小删除能力**

```python
    def delete_images(self, items: list[dict[str, object]]) -> dict[str, Any]:
        normalized_targets: dict[str, set[str]] = {}
        for item in items:
            if not isinstance(item, dict):
                continue
            record_id = str(item.get("record_id") or "").strip()
            image_ids = {
                str(image_id or "").strip()
                for image_id in item.get("image_ids") or []
                if str(image_id or "").strip()
            }
            if record_id and image_ids:
                normalized_targets.setdefault(record_id, set()).update(image_ids)

        if not normalized_targets:
            return {"deleted_images": 0, "deleted_records": 0, "items": self.list_records()}

        deleted_files: list[Path] = []
        deleted_images = 0
        deleted_records = 0

        with self._lock:
            next_records: list[dict[str, Any]] = []
            for record in self._records:
                record_id = str(record.get("id") or "").strip()
                target_ids = normalized_targets.get(record_id)
                if not target_ids:
                    next_records.append(record)
                    continue

                next_images: list[dict[str, Any]] = []
                for image in record.get("images") or []:
                    if not isinstance(image, dict):
                        continue
                    image_id = str(image.get("id") or "").strip()
                    if image_id in target_ids:
                        image_path = self.image_dir / str(image.get("file_name") or "").strip()
                        if image_path.is_file():
                            deleted_files.append(image_path)
                        deleted_images += 1
                        continue
                    next_images.append(image)

                if not next_images:
                    deleted_records += 1
                    continue

                next_record = {
                    **record,
                    "images": next_images,
                    "image_count": len(next_images),
                }
                next_records.append(next_record)

            self._records = next_records
            self._save_records()

        for image_path in deleted_files:
            if image_path.exists():
                image_path.unlink()

        return {
            "deleted_images": deleted_images,
            "deleted_records": deleted_records,
            "items": self.list_records(),
        }
```

- [ ] **Step 4: 运行服务层测试，确认转绿**

Run: `uv run pytest test/test_image_history_service.py -q`

Expected: PASS，输出包含：

```text
4 passed
```

- [ ] **Step 5: 提交服务层测试与实现**

```bash
git add test/test_image_history_service.py services/image_history_service.py
git commit -m "feat: add image history deletion service"
```

### Task 2: 为删除接口写失败测试并实现 FastAPI 端点

**Files:**
- Modify: `test/test_api_image_history.py`
- Modify: `services/api.py`

- [ ] **Step 1: 写失败测试，覆盖鉴权、批量删除和删空记录**

```python
    def test_image_history_delete_requires_authentication(self) -> None:
        record = self.history_service.list_records()[0]
        image_id = record["images"][0]["id"]

        with patch.object(api_module, "image_history_service", self.history_service), patch.object(
            api_module,
            "start_limited_account_watcher",
            return_value=_FakeThread(),
        ):
            with TestClient(api_module.create_app()) as client:
                response = client.post(
                    "/api/image-history/delete",
                    json={"items": [{"record_id": record["id"], "image_ids": [image_id]}]},
                )

        self.assertEqual(response.status_code, 401)

    def test_image_history_delete_returns_latest_items(self) -> None:
        second = self.history_service.save_record(
            source_endpoint="/v1/images/generations",
            mode="generate",
            model="gpt-image-1",
            prompt="第二条",
            image_items=[{"b64_json": PNG_B64, "revised_prompt": "第二条"}],
            usage={"input_tokens": 1, "output_tokens": 1056, "total_tokens": 1057},
        )
        first = self.history_service.list_records()[1]
        image_id = first["images"][0]["id"]
        headers = {"Authorization": f"Bearer {api_module.config.auth_key}"}

        with patch.object(api_module, "image_history_service", self.history_service), patch.object(
            api_module,
            "start_limited_account_watcher",
            return_value=_FakeThread(),
        ):
            with TestClient(api_module.create_app()) as client:
                response = client.post(
                    "/api/image-history/delete",
                    headers=headers,
                    json={"items": [{"record_id": first["id"], "image_ids": [image_id]}]},
                )

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertEqual(payload["deleted_images"], 1)
        self.assertEqual(payload["deleted_records"], 1)
        self.assertEqual([item["id"] for item in payload["items"]], [second["id"]])
```

- [ ] **Step 2: 运行 API 测试，确认路由缺失时失败**

Run: `uv run pytest test/test_api_image_history.py -q`

Expected: FAIL，报错类似：

```text
AssertionError: 404 != 200
```

- [ ] **Step 3: 在 `services/api.py` 中新增请求模型和删除端点**

```python
class ImageHistoryDeleteItemRequest(BaseModel):
    record_id: str = Field(default="")
    image_ids: list[str] = Field(default_factory=list)


class ImageHistoryDeleteRequest(BaseModel):
    items: list[ImageHistoryDeleteItemRequest] = Field(default_factory=list)
```

```python
    @router.post("/api/image-history/delete")
    async def delete_image_history(
        body: ImageHistoryDeleteRequest,
        authorization: str | None = Header(default=None),
    ):
        require_auth_key(authorization)
        delete_items = [
            {
                "record_id": str(item.record_id or "").strip(),
                "image_ids": [str(image_id or "").strip() for image_id in item.image_ids if str(image_id or "").strip()],
            }
            for item in body.items
            if str(item.record_id or "").strip()
        ]
        result = image_history_service.delete_images(delete_items)
        if result["deleted_images"] <= 0:
            raise HTTPException(status_code=404, detail={"error": "images not found"})
        return result
```

- [ ] **Step 4: 运行 API 测试，确认接口转绿**

Run: `uv run pytest test/test_api_image_history.py -q`

Expected: PASS，输出包含：

```text
4 passed
```

- [ ] **Step 5: 提交 API 测试与实现**

```bash
git add test/test_api_image_history.py services/api.py
git commit -m "feat: add image history delete api"
```

### Task 3: 接入前端删除 API 并实现历史页批量管理模式

**Files:**
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/app/history/page.tsx`

- [ ] **Step 1: 在 API 客户端增加删除请求类型与 helper**

```ts
type ApiImageHistoryDeleteItem = {
  record_id: string;
  image_ids: string[];
};

type ApiImageHistoryDeleteResponse = {
  deleted_images: number;
  deleted_records: number;
  items: ApiImageHistoryRecord[];
};

export async function deleteImageHistoryImages(items: ApiImageHistoryDeleteItem[]) {
  return httpRequest<ApiImageHistoryDeleteResponse>("/api/image-history/delete", {
    method: "POST",
    body: { items },
  });
}
```

- [ ] **Step 2: 先在历史页引入新 helper 和管理状态，再跑类型检查确认迁移点**

```ts
import {
  deleteImageHistoryImages,
  fetchImageHistory,
  fetchImageHistoryImage,
  type ApiHistoryImage,
  type ApiImageHistoryRecord,
} from "@/lib/api";
```

```ts
  const [isManageMode, setIsManageMode] = useState(false);
  const [selectedImageKeys, setSelectedImageKeys] = useState<string[]>([]);
  const [isDeleting, setIsDeleting] = useState(false);
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
```

Run: `npm run typecheck`

Expected: FAIL，报错聚焦在 `selectedImageKeys` 的消费、未实现的删除 handler 或 JSX 分支未补齐。

- [ ] **Step 3: 实现历史页批量管理模式**

```ts
function buildSelectionKey(recordId: string, imageId: string) {
  return `${recordId}:${imageId}`;
}

function revokeMissingObjectUrls(
  cache: Record<string, string>,
  records: ApiImageHistoryRecord[],
) {
  const validKeys = new Set(
    records.flatMap((record) => record.images.map((image) => buildCacheKey(record.id, image.id))),
  );
  for (const [key, url] of Object.entries(cache)) {
    if (!validKeys.has(key)) {
      URL.revokeObjectURL(url);
      delete cache[key];
    }
  }
}
```

```ts
  const pageSelectionKeys = pageRecords.flatMap((record) =>
    record.images.map((image) => buildSelectionKey(record.id, image.id)),
  );
  const selectedImageCount = selectedImageKeys.length;

  const toggleImageSelection = (recordId: string, imageId: string) => {
    const key = buildSelectionKey(recordId, imageId);
    setSelectedImageKeys((current) =>
      current.includes(key) ? current.filter((item) => item !== key) : [...current, key],
    );
  };

  const handleDeleteSelectedImages = async () => {
    const grouped = new Map<string, string[]>();
    for (const key of selectedImageKeys) {
      const [recordId, imageId] = key.split(":");
      if (!recordId || !imageId) continue;
      const current = grouped.get(recordId) ?? [];
      current.push(imageId);
      grouped.set(recordId, current);
    }
    if (grouped.size === 0) return;

    setIsDeleting(true);
    try {
      const payload = await deleteImageHistoryImages(
        Array.from(grouped.entries()).map(([record_id, image_ids]) => ({ record_id, image_ids })),
      );
      revokeMissingObjectUrls(objectUrlRef.current, payload.items);
      setRecords(payload.items);
      setSelectedImageKeys([]);
      setDeleteConfirmOpen(false);
      if (payload.items.length === 0) {
        setIsManageMode(false);
      }
      toast.success(`已删除 ${payload.deleted_images} 张图片`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "删除图片失败";
      toast.error(message);
    } finally {
      setIsDeleting(false);
    }
  };
```

```tsx
<div className="flex flex-wrap gap-3">
  {isManageMode ? (
    <>
      <Button type="button" variant="outline" onClick={() => setSelectedImageKeys(pageSelectionKeys)}>
        全选当前页
      </Button>
      <Button type="button" variant="outline" onClick={() => setSelectedImageKeys([])}>
        清空选择
      </Button>
      <Button type="button" onClick={() => setDeleteConfirmOpen(true)} disabled={selectedImageCount === 0 || isDeleting}>
        删除所选
      </Button>
      <Button
        type="button"
        variant="ghost"
        onClick={() => {
          setIsManageMode(false);
          setSelectedImageKeys([]);
        }}
      >
        取消管理
      </Button>
    </>
  ) : (
    <Button type="button" variant="outline" onClick={() => setIsManageMode(true)}>
      批量管理
    </Button>
  )}
</div>
```

```tsx
{record.images.map((image) => {
  const selectionKey = buildSelectionKey(record.id, image.id);
  const selected = selectedImageKeys.includes(selectionKey);
  return (
    <button
      key={image.id}
      type="button"
      onClick={() => (isManageMode ? toggleImageSelection(record.id, image.id) : void openRecordLightbox(record))}
      className={cn(
        "relative overflow-hidden rounded-2xl",
        isManageMode && selected ? "ring-2 ring-stone-950" : "",
      )}
    >
      {isManageMode ? <span className="absolute top-2 left-2 rounded-full bg-white px-2 py-1 text-xs">{selected ? "已选" : "选择"}</span> : null}
      <img src={thumbnailUrl} alt={record.prompt || "API 生成图片"} className="h-full w-full object-cover" />
    </button>
  );
})}
```

- [ ] **Step 4: 运行前端静态检查与构建验证**

Run:

```bash
npm run lint
npm run typecheck
npm run build
```

Expected:

- `npm run lint` 通过，允许保留当前项目已有 warning，但不新增 error
- `npm run typecheck` 通过
- `npm run build` 通过

- [ ] **Step 5: 提交前端改动**

```bash
git add web/src/lib/api.ts web/src/app/history/page.tsx
git commit -m "feat: add bulk image management to api history"
```

### Task 4: 全量验证并补最终手工检查

**Files:**
- Verify only

- [ ] **Step 1: 跑后端全量测试**

Run: `uv run pytest`

Expected: PASS，输出包含：

```text
collected 19 items
... all passed ...
```

- [ ] **Step 2: 跑前端三项验证**

Run:

```bash
npm run lint
npm run typecheck
npm run build
```

Expected:

- `lint` 无 error
- `typecheck` 成功
- `build` 成功

- [ ] **Step 3: 本地启动后做最小手工验收**

Run backend:

```bash
uv run uvicorn main:app --host 127.0.0.1 --port 8000
```

Run web:

```bash
npm run dev
```

手工检查：

1. 登录并进入 `/history`
2. 点击 `批量管理`
3. 只能选中当前页图片
4. 选中跨记录图片后点击 `删除所选`
5. 确认弹层出现并能取消
6. 确认删除后，图片消失、删空记录的卡片消失
7. 顶部统计与分页正确刷新

- [ ] **Step 4: 提交最终收尾**

```bash
git add services/image_history_service.py services/api.py test/test_image_history_service.py test/test_api_image_history.py web/src/lib/api.ts web/src/app/history/page.tsx
git commit -m "feat: support bulk deleting api history images"
```
