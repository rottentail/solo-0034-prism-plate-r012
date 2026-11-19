# HTTP契约

所有写入场景的JSON入口拒绝未知字段和尾随内容，最大8MiB。服务返回JSON错误；404为对象不存在，409为版本或生命周期冲突，429为成像容量不足，400为不符合输入契约，504为超时。未知路由由HTTP框架返回404。

| 方法与路径 | 输入及结果 |
|---|---|
| GET /healthz | ready |
| POST /v1/plates | id,title,document →201版面 |
| GET /v1/plates/{id} | 完整稿本历史 |
| PUT /v1/plates/{id} | expected_revision,document →追加稿本 |
| POST /v1/plates/{id}/branch | revision,id,title →201独立版面 |
| POST /v1/plates/{id}/archive | 禁止修订；历史仍可读取 |
| POST /v1/plates/{id}/boolean | 从指定稿本的闭合轮廓做并集/相交/差集，追加为可继续变换的新稿本 |
| POST /v1/plates/{id}/render?revision=1&format=png | 201成品元数据；format也可为svg |
| POST /v1/plates/{id}/exposures?revision=1&format=png | 202瞬态任务 |
| GET /v1/exposures/{id} | queued/running/succeeded/failed/cancelled |
| DELETE /v1/exposures/{id} | 202取消请求 |
| GET /v1/artifacts/{id} | 图片；ETag为成品字节摘要；X-Scene-Digest为稿本摘要 |
| DELETE /v1/artifacts/{id} | 撤销成品 |
| POST /v1/proofs | id,artifact →201 open校样 |
| PUT /v1/proofs/{id} | state为accepted或rejected，note为说明 |
| GET /v1/plates/{id}/bundle | 含完整稿本历史的封包JSON |
| POST /v1/bundles | 原样封包；201导入，重名409 |

成品响应中的data为null，避免重复传输二进制；GET成品获得实际内容。If-None-Match精确匹配强ETag时返回304。成功校样可从持久化快照确认，当前版本没有校样列表/详情接口。GET版面返回完整历史，服务没有全局列表接口。

## 轮廓布尔运算

`POST /v1/plates/{id}/boolean` 在服务端把若干已编译形状组合成新的闭合轮廓，并作为一个新的不可变稿本追加（旧稿本不变）。结果形状放在新增图层中，使用单位变换与 evenodd 填充规则，因此它本身仍是可继续变换的普通形状，能再次参与渲染或后续布尔运算。

请求字段（未知字段一律 400）：

| 字段 | 含义 |
| --- | --- |
| expected_revision | 乐观并发用的当前稿本序号；不符返回 409 |
| source_revision | 抽取操作数轮廓的稿本序号；不存在返回 404 |
| operation | `union`、`intersect`、`difference` 之一；差集用第一个操作数减去其余 |
| operands | 至少两个 `{layer, shape}` 引用，按 `layer`/`shape` 的 id 定位；重复或缺失返回 400 |
| brush_source | 结果外观选择：`first`（首个操作数笔刷）、`last`（末个操作数笔刷）、`explicit`（需同时给 brush） |
| brush | brush_source=explicit 时必填，结构与形状的 brush 契约一致 |
| tolerance | 文档坐标系下的焊接容差，范围 1e-12..1，省略时取 1e-9 |
| layer_id, shape_id | 结果图层与形状的 id，须符合标识符规则且不与源稿本已有节点重名 |
| title | 可选，最长 160 |

运算在文档坐标系进行，操作数自身的仿射变换（含镜像、错切、平移）与填充规则（nonzero/evenodd，含镂空与反向绕向）都会被纳入。重合边在平面细分时焊接成同一条图边，因此重叠边界不会产生碎片；真正的窄通道在容差足够小时保留为独立轮廓。生成的文档与手写场景走同一套 `validate.Compile` 准入，容量、段数预算和有界工作量限制同样适用。
