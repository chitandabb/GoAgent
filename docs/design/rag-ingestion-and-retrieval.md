# MESGuard 第三阶段：混合文档解析与 Agentic RAG

## 文档状态

- 状态：`M2-A1 至 M2-A8 后端链路已完成 / M2-B1 部分完成 / M2-B2 实现完成待真实 smoke`
- 简历主线：第三条“混合文档解析与 Agentic RAG”
- 用途：记录本阶段已验证基线、架构决策、反对意见、评测口径和未决问题
- 规则：未在“已确认决策”中出现的内容都不能视为实现承诺或简历事实

## 当前已验证基线

当前仓库已经具备：

- `knowledge_documents`、不可变 `knowledge_document_versions` 和可追溯 `knowledge_chunks`；
- `global`/`personal` 可见范围及 PostgreSQL 查询内权限过滤；
- Markdown/纯文本的确定性章节、段落、表格分块；
- 超长块按字符上限、自然边界和 overlap 进行兜底切分；
- PostgreSQL FTS 与固定 `rag-retrieval-v1` 评测；
- 12 份工业文档、24 条 literal/paraphrased 查询，FTS Recall@5 为 23/24（95.83%），MRR 为 0.9028；
- 唯一漏召回的 ERP 504 语义改写样本保留给向量检索补位验证。
- MinIO 双私有 Bucket、不可变对象写入、流式 SHA-256、对象引用和上传失败补偿删除；
- queued 知识版本、可恢复入库任务/事件、`knowledge.ingest` Outbox 与 RabbitMQ 持久化 Queue；
- queued version、pending task、首个 event 和 Outbox 的 PostgreSQL 原子创建；
- 独立 Knowledge Worker、Element Artifact、fenced Chunk staging 与原子 `ready/current` 发布；
- UTF-8 TXT/Markdown、嵌入文本 PDF、DOCX、XLSX、PPTX 的确定性解析；
- PDF 无文本页、PNG/JPEG 独立图片和 Office media 的受限视觉资产提取、引用定位及后端路由；
- Artifact schema v5 的视觉/版面/provider usage/Element合并 provenance、`partial_ready` 降级和视觉-only 永久失败语义；
- 固定 PP-DocLayout-M/ONNX Runtime 契约、PDFium-WASM 页面渲染、区域级 OCR/VLM 显式路由与整页重复调用抑制；
- PDF 页数/文本预算以及 OOXML 条目、展开大小、XML、工作表行列等资源边界；
- PDF/DOCX/XLSX/PPTX 真实服务链路 smoke，均验证 Parser 版本、Chunk 和 Artifact SHA-256。
- Embedding Profile、批量 Embedding、pgvector 精确向量检索、FTS/Vector 并行召回和 RRF 融合；
- 可选 DashScope `qwen3-rerank` 适配器、Rerank Token/延迟观测和失败时保留 RRF 的降级；
- 最终 child 命中后的同版本/同章节有界上下文扩展，独立返回邻接 Chunk 身份与哈希；
- Parent 扩展后的确定性整 Chunk 压缩，全局限制邻接 Chunk/rune，并输出输入、输出和省略统计；
- 受控 Query Plan、protected signals 门禁、多 Query 通道内合并和失败时回退原 Query；
- 后端自动授权诊断任务使用知识库，统一 `search_knowledge` Tool 隐藏检索通道、候选预算和对象存储细节；
- Evidence Gate 只在事实证据缺口时允许第二轮最多一次 `search_knowledge`，并记录是否新增证据和停止原因；
- Agentic 二次检索三 Case 真实模型固定集，覆盖证据缺口、纯格式修复和首轮通过三条控制路径；
- Knowledge Chunk 结果的文档/版本/Chunk UUID、内容哈希、标题、页码和章节路径校验；空结果或损坏结果不会生成可引用 EvidenceItem；
- `rag-retrieval-v1` 固定集的 FTS、Vector、RRF 和 RRF+Rerank 对照结果，以及布局/OCR/VLM/PPTX 的独立质量记录。

当前尚未实现：

- 附件领域表以及附件上传/内容代理读取 API；
- 内容代理读取、孤儿清理和完整对象生命周期；
- 更大规模 OCR/VLM provider 配对测评、扫描表格/小字体/退化扫描质量、复杂图表区域语义和丰富 Office 视觉语义；
- XLSX 公式表达式、隐藏 Sheet/Slide 语义和 PPTX 演讲者备注等丰富 Office 语义；
- 物化 Parent 索引和面向用户的知识问答 HTTP/SSE 运行链路；受控 Query Rewrite 与逻辑 Parent
  邻接扩展各完成一个 paired Case，Compression 质量轴已完成 5 Case，另有单压力 Case 触发生产阈值；
  Agentic 二次检索已完成三 Case 真实模型决策固定集，但答案质量、失败/重复证据和多轮稳定性仍待扩展；
- Web Search Tool、公开页面抓取和 `web` 引用链已实现；真实 Firecrawl smoke 与公网问答质量固定集仍待 Key/额度验证；
- MinIO/RabbitMQ/PostgreSQL/pgvector 全链路的文档处理吞吐量基线和最终第三条简历指标；当前 parser-only 吞吐与 routing avoidance 不能直接写成端到端提升。

## 目标边界

本阶段需要同时服务：

1. 企业全局知识文档；
2. 用户个人知识文档；
3. 后续聊天消息附件；
4. 工单诊断任务附件及其证据引用；
5. Agent 在权限、预算和证据门禁内按需调用 RAG，而不是把全部检索结果预先塞入上下文。

## 已确认决策

1. 生产目标允许调用公网模型服务，不要求完全离线；模型、OCR、VLM、Embedding 和 Rerank
   供应商必须配置化，不能把某一家云服务写死在领域逻辑中。
2. Go 是业务编排和核心后端语言，但允许把成熟的文档解析、OCR 或多模态能力作为独立容器、
   CLI 或 HTTP 服务接入，不要求用 Go 重写底层算法。
3. 首版格式范围保留 TXT/Markdown、PDF、扫描 PDF、DOCX、XLSX、PPTX、PNG/JPEG。
4. 初始容量边界采用单文件 50 MiB、最多 500 页、单批次 20 个文件、单用户最多 2 个处理中
   批次；后续只有评测和真实容量证据才能调整。
5. 默认部署环境按无 NVIDIA GPU、Windows 宿主机和中低配置服务器设计；GPU 只能作为可选加速，
   不能成为正确性依赖。
6. OCR/VLM 或部分解析依赖不可用时，文档进入 `partial_ready`，保留已成功提取的内容和明确的
   缺失元素；依赖恢复后允许幂等补处理，不能把缺失图片证据伪装成完整文档。
7. 吞吐量配对实验暂定：baseline 为单 Worker 串行完成解析、切块、Embedding 和写库；
   experiment 为异步流水线、有限并行解析、批量 Embedding 和批量写库。
8. 保留底层 `personal` scope、权限隔离和测试，但本阶段不交付完整个人知识库 UI/API；优先完成
   简历第三条直接需要的企业知识与会话/诊断附件链路。
9. MinIO 使用两个私有 bucket：附件与知识原文分离生命周期；object key 由服务端生成，原始
   文件名只保存在 PostgreSQL，前端和模型不接触 bucket、object key 或 MinIO 凭据。
10. `Attachment` 与 `KnowledgeDocument` 是两个领域对象，共享对象存储和解析流水线；临时附件
    不自动成为知识文档。
11. 本阶段保留底层 `personal` scope 和权限测试，但不交付完整个人知识库管理 UI/API；聊天和
    诊断附件首版使用 `session`，企业知识由管理员发布到 `global`。
12. 首版上传采用浏览器经 MESGuard API 流式代理到 MinIO，服务端同时计算 SHA-256；50 MiB
    上限内不实现预签名直传、客户端 Multipart 或断点续传。
13. 不做跨用户对象去重；附件、文档版本和历史证据使用独立不可变对象。
14. RAG 增量更新必须纳入设计：同一逻辑文档内容变化时创建新版本和新原始对象，不覆盖旧版；
    新版完成前旧版继续提供检索，发布时原子切换当前版本。
15. 工单附件同时支持用户补交附件和 ERP 工单已有附件；模型可以在首次诊断证据不足时声明
    缺少的证据，用户补交附件后发起带附件的新一轮诊断。
16. 接受恶意文件扫描采用 `disabled|optional|required` 配置；`required` 下扫描失败、超时或
    扫描器不可用时对象必须保持 `quarantined`，不能进入解析、预览或检索。
17. 首版先实现 `MalwareScanner` 端口、扫描状态、确定性文件限制和解析隔离；ClamAV 或企业扫描
    服务作为可插拔适配器，不作为完成 RAG 主链路的前置部署条件。
18. 当前默认 ERP SQL Server 只保存附件元数据和 object key，真实文件保存在企业 MinIO；
    `ExternalAttachmentReader` 首个生产适配器因此采用 MinIO/S3 API，不使用本地 fixture 冒充生产协议。
19. 首版保留现有逻辑文档版本和唯一 current，但不实现按业务日期检索历史制度；正文日期只作为
    候选元数据，不能根据解析完成时间或模型抽取日期自动切换 current。
20. ERP 附件首次被诊断任务或报告作为证据使用时，必须固化到 MESGuard 私有附件 Bucket；同一
    MinIO 实例优先使用服务端 CopyObject，并记录源引用、ETag/version id、SHA-256 和固化对象引用。
21. 接受“版本化全量快照 + 分阶段增量计算 + Chunk 级增量索引”：新版保留完整 Chunk 快照，
    只对发生变化的 OCR/VLM/Embedding 结果重新计算，未变化内容按版本化缓存复用。
22. 混合文档解析目标是可检索、可引用、可复核，不实现 Office/PDF 像素级还原；本地确定性解析
    为基础，OCR/VLM 使用配置化企业云服务，本地 OCR 只作为可插拔适配器。
23. PDF 采用页面级粗分类和元素级细分流，同一页允许原生文本、表格恢复、区域 OCR 和 VLM
    描述并存；处理结果按页面区域、来源优先级和文本相似度去重，不能把整页强制三选一。
24. VLM 初始预算为每文档最多 30 个视觉元素，并同时限制总像素、单次超时和费用；超限元素
    写入缺失声明，最终阈值必须由混合文档评测调整。
25. 接受首版 Office/PDF 边界：拒绝加密 PDF；DOCX 提取标题、正文、表格和图片但不执行宏；
    XLSX 处理可见 Sheet 的显示值、表头和公式文本但不计算公式；PPTX 处理可见幻灯片、文字、
    表格、图片和演讲者备注；隐藏 Sheet/Slide 忽略并记录警告。
26. 接受解析质量门禁：核心正文可用但少量视觉元素失败可为 `partial_ready`；正文无法提取或结果
    为空为 `failed`；安全状态未知为 `quarantined`；新版 `partial_ready` 不自动替换 current。
27. 版本化 `DocumentElement` JSON artifact 保存在 MinIO；PostgreSQL 保存任务/版本状态、artifact
    引用与哈希、Chunk、索引和证据定位，首版不为每个底层元素建立独立关系表。
28. 分块从字符计数升级为 `TokenCounter`：child 目标 300-500 tokens、硬上限 800，低于约 80
    tokens 的相邻同章节文本优先合并；阈值是固定语料评测初值，不是不可调整的业务常量。
29. 语义切割只用于弱结构长正文，Embedding 不可用或超预算时降级为结构和句子边界；采用
    child 检索、parent 按需展开，parent 初始目标为 800-1500 tokens。
30. overlap 只用于被硬切开的连续正文，初值 10%-15%；表格按重复表头和完整行分块，图片描述
    与标题/图注形成原子 child，标题边界、不同元素和跨页内容不机械重叠。
31. 每版本 5,000 Chunk 告警、10,000 Chunk 硬上限；超过硬上限整版失败并保留统计，不能静默
    截断发布。分块策略通过 A/B/C/D 固定语料消融实验决定是否保留额外复杂度。
32. 新增独立 `[models.embedding]` 配置、`Embedder` 端口和版本化 `EmbeddingProfile`；首个 POC
    可使用百炼 `text-embedding-v4`，但必须通过真实 API 确认维度、批量限制和输入模式后再建迁移。
33. 普通检索同一时刻只使用一个 active Embedding Profile；模型升级采用后台回填、固定集评测、
    原子切换和限期保留旧 Profile 回滚，不改变业务文档版本。
34. pgvector 首版使用精确余弦检索；只有规模压测证明不满足延迟目标时才评估 HNSW，并单独验证
    权限、scope、session 和 current 过滤后的 Recall@K。
35. FTS 与 Vector 初始各取 top 20，使用初始 `k=60`、等权的 RRF 融合；候选数、权重和参数只
    能在验证集上调整。权限、范围和版本过滤必须进入 SQL，禁止全库召回后在 Go 层补过滤。
36. FTS 或 Vector 单路失败时允许降级并返回 `degraded` 与缺失通道；两路均失败才是系统失败，
    通道正常但无命中是业务空结果。Embedding Cache 仅复用相同 profile 下完全相同的规范化内容。
37. 新建诊断任务由后端策略自动授予并冻结 `knowledge` capability，前端和用户不选择 Tool；独立知识问答只获得 knowledge/attachment，不获得 SQL、GitHub 或工单能力。知识库依赖不健康时只隐藏对应 Tool，不改写任务快照。
38. Agent 只调用高层 `search_knowledge` Tool；Query 改写、FTS/Vector、RRF、Rerank、去重和
    parent 展开封装在 Tool 内，最多 2 轮检索、每轮最多 3 个子查询。
39. Query 改写采用规则保护关键字后由配置化模型生成独立问题；对话只提供最近消息和版本化摘要，
    Rerank 初始 top 30 -> top 8，不可用时保留 RRF 顺序并标记 degraded。
40. 知识回答必须展示引用来源，并执行事实结论到本次 Chunk 的引用门禁；引用至少包含文档标题、
    业务版本、页码/Sheet/Slide、章节和可定位的 chunk/sourceRef。
41. Web Search 默认可配置但不向普通用户展示 Tool 开关，仅在内部知识不足、需要最新公开资料或
    用户明确要求联网时调用；首个 `WebSearcher` 供应商采用 Firecrawl，Key 通过
    `FIRECRAWL_API_KEY` 注入。
42. Web 来源按 A=官方/标准/上游仓库、B=可信厂商文章、C=社区/博客分级；C 级来源不能独立支撑
    conclusive 诊断，Web 不能替代内部工单、数据库、代码和制度事实。
43. Web 调查初始预算为每轮 5 条结果、最多抓取 3 页、单页 20,000 字符、最多 2 轮；每条证据
    展示标题、URL、来源域、可得的页面时间、fetched_at 和截断/失败声明。
44. Web 页面只作为本次 Evidence 快照按审计期保留，不自动写入知识库；不可用时标记 degraded，
    不能把 ChatModel 训练记忆伪装成联网结果。
45. `rag-ingestion-v1` 至少包含 40 份、8 类格式，每个吞吐实验 variant 至少重复 5 次；主吞吐
    增幅只比较 cold 串行 baseline 与 cold 有界流水线 experiment，warm incremental 单独报告。
46. `rag-retrieval-v2` 至少包含 50 份文档和约 120 条人工标注查询，以 Chunk Recall 为主、
    Document Recall 为辅，并固定 train/dev/test，test 不参与阈值调优。
47. Answer/Citation 评测以人工黄金事实和确定性引用检查为事实源；独立 LLM Judge 只提供辅助
    评分，不允许生成模型自评，也不能因 Judge 不可用而改变核心指标。
48. 公开评测语料优先选择官方、可下载、许可边界明确的大型技术文档；派生 Office/扫描变体必须
    记录父内容哈希、生成器版本和许可证，不允许同一内容的不同格式跨 train/dev/test 泄漏。
49. 自部署能力限定为本地 ONNX 版面路由器，不自部署完整 Docling、OCR 或 VLM。Go 领域接口和
    RoutePlanner 负责规则/模型融合；Eino 仅提供可选 Transformer/Callback 适配，不成为 Artifact
    的事实模型。
50. 页面区域分割先于 OCR/VLM 识别，检索 Chunk 分割晚于所有 Element 识别、合并与去重。页面
    先判定 native/scanned/mixed，再按 text/table/picture/caption/formula 等区域细分流。
51. Docling 作为版面/表格设计参考、离线对照基线和可选低置信度兜底，不作为 M2-A7 的必需
    Python 运行时；布局模型权重、标签、许可证、SHA-256、预后处理版本和 ONNX Runtime 版本必须固定。
52. OCR 首版继续使用云端 `qwen-vl-ocr-latest`，本地 OCR 仅保留端口，不阻塞简历第三条；VLM
    第一轮在相同裁剪区域上比较 `step-3.7-flash` low reasoning 与 `qwen3-vl-plus`，不按厂商 TPS
    宣传直接切换生产模型。三张真实图示的首轮成对测评中，两者在 2,048 输出 Token 下都通过
    3/3 严格 JSON、100% 文本锚点和 8/9 关系事实；人工复核各发现一处关系错误。Qwen 平均延迟
    低 32.1%、总 Token 少 53.5%，因此继续作为生产 Vision 配置，StepFun 保留候选。
53. VLM 主指标是端到端区域吞吐、P50/P95、严格 JSON 首次成功率、图表/关系语义准确率、限流、
    错误率和单个成功区域成本；`gemini-3.5-flash-lite` 仅在凭证可用时作为速度参考。
54. ONNX Router 需要单独报告页面/区域 Macro-F1、高价值视觉元素漏检率、CPU/RAM 和避免的
    OCR/VLM 调用数；路由降本不能以降低最终 Recall@5 或引用完整性为代价。
55. Parent 邻接上下文采用确定性整 Chunk 压缩，不用 LLM 摘要改写证据。生产初值为全局最多
    6 个邻接 Chunk、3000 rune、最低相关分 0.05；命中 Chunk 不计入该预算，配置不能由 Tool 参数覆盖。
56. Evidence Gate 的第二轮只在缺少可追溯证据或来源绑定失败时允许 `search_knowledge`，最多一次；
    纯结构/格式缺口隐藏该 Tool。二次检索是否尝试、是否新增稳定版本/Chunk/内容哈希和停止原因必须落库并可查询。

## 模型 Provider 可替换性边界

“配置里有 provider/model/baseURL”不等于已经热插拔。MESGuard 只在下面三个条件同时满足时宣称
某个角色支持配置切换：领域层依赖稳定接口、Bootstrap 已注册目标 Provider 适配器、目标模型通过
该角色的真实契约 smoke 和 paired 质量评测。当前状态如下：

| 模型角色 | 当前实现 | 只改配置可换 Provider | 切换时必须处理 |
| --- | --- | --- | --- |
| Agent Chat | 命名 Profile Resolver + 通用 Factory；已注册 StepFun/DeepSeek/DashScope Adapter | 装配层可以；目标 Profile 仍需准入 | Tool Calling、推理参数、Usage、取消和 Agent paired baseline |
| Query Rewrite | 独立 `modelProfile`，不再复用主 Agent；默认候选 `qwen-rewrite` | 装配层可以；当前仍默认关闭 | 严格 JSON、protected signals、质量净收益、P95 和每千次成本 |
| LLM Judge | 只有默认关闭的 DashScope 配置骨架 | 否 | 独立适配器、Judge Prompt/Schema、与人工黄金事实的一致性校准 |
| Embedding | `Embedder` 接口，DashScope `text-embedding-v4` 适配器 | 否，且不能原地换向量空间 | 新 `EmbeddingProfile`、全量回填、固定集评测、原子 active 切换和旧 profile 回滚 |
| Rerank | `Reranker` 接口，DashScope `qwen3-rerank` 适配器 | 否 | 请求/分数契约、候选上限、超时降级和排序质量/成本 paired 评测；不需要重建向量 |
| OCR / VLM | 通用 Generator/Processor 端口，Bootstrap 固定 DashScope | 否 | 图片输入格式、严格 JSON、Prompt、视觉质量、限流和 Token/费用字段映射 |
| Layout Router | `LayoutRouter` 接口，固定 ONNX Runtime/PP-DocLayout-M 契约 | 否 | 权重许可证/SHA、标签映射、预后处理、固定集 Macro-F1 和资源门禁 |

Chat Profile 的解析链路是 `role/profile -> Profile Resolver -> Provider Factory -> Adapter -> Eino
ToolCallingChatModel`。静态校验覆盖所有已配置 Profile，但只有 active 或被角色引用的 Profile 才读取
对应 API Key。StepFun Adapter 映射 `reasoning_effort`；DeepSeek 要求显式 `thinking`，关闭 Thinking
时拒绝 effort，开启时映射其 `low/high/xhigh/max` 并标记多轮 Tool Calling 需要保留
`reasoning_content`；DashScope 把 `thinkingMode` 映射为 `enable_thinking`。不支持的组合启动/构建即失败，
不会静默忽略。

Chat 的 ReAct 循环、TaskScope、Tool 参数策略和 Evidence Gate 不依赖 StepFun；换模型不会获得额外
权限，也不会改变确定性门禁。Provider 差异集中在模型能力：有的模型支持 `reasoning_effort`，有的
通过模型 ID 或其他字段控制推理，有的完全不支持该旋钮。适配器必须显式映射、忽略或拒绝，不能
把 `low/medium/high` 盲传给所有 OpenAI-compatible endpoint。Eino OpenAI 扩展提供自定义 `BaseURL`、
`ReasoningEffort` 和额外请求字段，这证明可以实现适配器，不证明任意兼容端点语义相同。

Prompt/Completion/Total Token 继续以供应商 Usage 为准，不从字符数估算。Cached/Reasoning Token
只有供应商返回对应明细且适配器完成映射时才可用于指标；当前值为 0 还不能区分“真实为 0”和
“未报告”。总 Token 预算是收到每次调用 Usage 后结算并阻止后续运行的运行时预算，不是跨 Provider
精确预分词或费用预授权。切换 Chat Provider 后必须创建新的 model/provider baseline，不能和 StepFun
旧样本直接合并比较。

参考：CloudWeGo Eino Extension 官方 OpenAI ChatModel 文档：
`https://github.com/cloudwego/eino-ext/blob/main/components/model/openai/README.md`。

### 2026-08-06 DeepSeek Chat 迁移审计

DeepSeek 官方当前 Chat Completion 接口使用 Bearer API Key 和 OpenAI-compatible message/tool
结构，官方客户端示例将 `base_url` 设为 `https://api.deepseek.com`；Chat 请求仍使用
`max_tokens`，支持 `response_format={"type":"json_object"}` 和流式响应。当前文档列出的模型包括
`deepseek-v4-pro` 与 `deepseek-v4-flash`，思考模式示例同时使用 `reasoning_effort` 和
`thinking={"type":"enabled"}`。模型名和请求参数属于供应商演进面，部署时必须以当时文档和
`/models` 响应为准，不能把这里的名称当成永久常量。

思考模式下的 Tool Calling 不是普通 OpenAI 请求的无差别替换：DeepSeek 官方示例要求把模型返回的
assistant message（包括 `reasoning_content` 和 Tool Calls）加入下一轮消息，再追加 Tool 结果。仓库
当前 Eino `v0.9.13`、OpenAI component `v0.1.13` 和 ACL `v0.1.17` 的本地源码能够解析并重新发送
`ReasoningContent`，也支持 `ExtraFields`；这说明适配器具备实现基础，不等于已经通过 DeepSeek
真实 Agent 循环。

2026-08-07 已完成配置和请求装配层迁移：

| 位置 | 当前行为 | 剩余验收 |
| --- | --- | --- |
| `ChatModelConfig` | `activeProfile + profiles.<name>`；StepFun/DeepSeek/DashScope 静态校验 | 配置变更/密钥轮换仍需重启，尚无动态热加载 |
| `chatmodel` | 通用 Factory + 三个 Adapter；离线请求形状测试覆盖 effort/thinking/max token | DeepSeek non-thinking/thinking 真实 Tool Calling 和流式探针 |
| `defaultAgentRuntimeBuilders` | active Profile 创建主 Agent；模型身份来自解析后的 Profile | 切换后跑 Agent paired baseline |
| Query Rewrite | 按独立 `modelProfile` 创建小模型，构建失败只降级 `query_rewrite` | Qwen 小模型单 Case 成本/延迟/质量对照 |
| `mesguard-rag-paired-observe` | Rewrite arm 与生产复用相同 named-profile Factory | 批量运行前仍需人工预算确认 |
| Usage | 标准 Prompt/Completion/Total 已归一化 | 探针确认 cached/reasoning 明细；供应商专有 cache 字段不能静默当成 0 |
| 评测数据 | StepFun baseline 已存在 | DeepSeek 使用独立 Provider/Model baseline，不与旧 observation 混合 |

下一步不再改业务编排，而是用一个无副作用 Tool 完成 DeepSeek non-thinking 与 thinking 的非流式
多轮 Tool Calling；随后验证严格 JSON、流式最终 usage、取消和 Evidence Gate；最后在固定 Case 上
与 StepFun 做 paired 质量、Token、P50/P95 和失败率对照。未完成这些探针前，只能说“Provider
装配可配置且请求合同已验证”，不能说“DeepSeek 已通过生产热插拔验收”。active Profile 仍是
`stepfun-main`。

官方资料：

- `https://api-docs.deepseek.com/`；
- `https://api-docs.deepseek.com/api/create-chat-completion`；
- `https://api-docs.deepseek.com/guides/thinking_mode`。

## 第一轮：产品与部署边界

以下问题必须先确认，答案会决定 MinIO bucket、对象生命周期、解析 Worker、OCR/VLM 和评测环境：

1. 生产目标是企业内网私有化部署、可访问公网的单租户部署，还是需要同时支持两者？
2. 文档和附件是否可能包含客户名称、数据库内容、日志、源代码或其他禁止发往公网的敏感信息？
3. 首版必须支持哪些格式和规模上限：Markdown/TXT、PDF、DOCX、XLSX、PPTX、PNG/JPEG、扫描 PDF；单文件和单批次上限分别是多少？
4. “聊天附件”和“工单附件”默认是仅本次会话使用、保存到个人知识库，还是必须由用户显式选择；全局知识库是否仅管理员可发布？
5. 本地部署可假设有 NVIDIA GPU 吗；若没有，CPU 核数、内存和可接受的单文档处理时间是什么？
6. 是否允许配置云端 OCR/VLM/Embedding/Rerank 供应商作为可选能力；若允许，哪些数据经过脱敏后才能外发，谁负责审批？
7. 原始对象需要保留多久；用户删除、文档换版、任务/报告引用和合规审计之间的保留优先级是什么？
8. 第一阶段验收更看重“功能链路完整”，还是必须先拿到吞吐量提升 40% 和 Recall@5 90%+ 的可复现结果？

### 第一轮结论

- 允许使用企业采购或配置的公网模型服务，不要求默认阻止全部数据外发；仍需记录供应商、模型、
  数据类型和处理版本，以便审计与切换。
- Go 负责业务编排，允许依赖外部解析/OCR/VLM 服务。
- 格式和初始容量采用上述“已确认决策”。
- 个人知识库进入第二轮继续质询，不因已有 `personal` 数据字段就默认继续建设完整产品能力。

## 第二轮：MinIO、附件归属与生命周期

1. 个人知识库是否采用“保留底层 `personal` scope 和权限测试，但本阶段不实现个人文档管理
   UI/API”的收缩方案？聊天上传只支持 `session`，管理员上传才进入 `global` 知识库。
2. 是否接受“附件”和“知识文档”是两个领域对象，共享同一套对象存储和解析流水线，但不把
   每个临时聊天附件都自动创建为 `knowledge_document`？
3. 工单诊断附件的来源包含哪些：用户在诊断页主动上传、ERP 工单已有附件，还是两者都要？
   如果要读取 ERP 已有附件，上游当前通过什么接口或凭据下载，不能只依赖 SQL 中的 object key。
4. 首版上传是否采用浏览器 `multipart/form-data -> MESGuard API -> MinIO PutObject` 的后端流式
   代理；50 MiB 上限内不提前引入预签名直传和客户端 Multipart 完成协议？
5. 是否接受服务端生成不可预测 object key，原始文件名只保存在 PostgreSQL；前端和模型永远
   不接触 bucket、object key 或 MinIO 凭据？
6. 是否需要恶意文件扫描？若需要，首版可把 ClamAV 等扫描服务作为可降级外部依赖；扫描未
   通过前对象不能进入解析和检索。
7. 是否接受不做跨用户内容去重：即使 SHA-256 相同，不同附件仍有独立对象和权限，避免共享
   对象导致删除、审计和侧信道复杂化？
8. 删除和保留是否采用：未绑定对象 24 小时清理；会话附件按保留期清理；被任务、报告或证据
   引用的对象只允许逻辑删除；知识文档换版保留历史对象，不能覆盖旧版本？
9. 首版是否关闭 MinIO bucket versioning/object lock，依靠不可变 object key 和 PostgreSQL 版本
   事实保证历史；只有出现合规保留需求后再启用对象锁？

### 第二轮结论

- 个人知识库按收缩方案处理：保留底层 scope，不投入完整产品开发。
- `Attachment` 与 `KnowledgeDocument` 分离聚合，共享存储和解析基础设施。
- 用户主动上传和 ERP 工单已有附件都需要支持；ERP 文件字节读取方式仍待确定。
- 上传采用 API 流式代理；使用 `mesguard-attachments` 与 `mesguard-knowledge-originals` 两个私有
  bucket；object key 完全由服务端生成。
- 不做跨用户对象去重；接受未绑定对象、会话对象、证据对象和知识版本的分层保留规则。
- 恶意文件扫描是否作为首版运行依赖仍待确认；无论是否部署病毒库，都必须实施格式签名、
  解压规模、页数、像素、超时、外链和宏等确定性安全限制。
- MinIO Bucket Versioning/Object Lock 与业务文档版本需要分开决策。

## 增量更新原则

同一制度、手册或产品文档发生内容变化时：

1. 由稳定的 `knowledge_document.id` 表示逻辑文档身份，不能根据标题相似度自动合并文档；
2. 管理员明确执行“上传新版本”，创建新的 `knowledge_document_version` 和新的 MinIO object key；
3. 正确性基线仍对新版原文执行完整解析，避免 PDF/DOCX 局部二进制差分遗漏结构变化；
4. 对解析后的 section/element/chunk 计算稳定指纹，与上一版本比较；
5. 内容和 Embedding profile 均未变化的 Chunk 可以复用 Embedding 结果，新增或修改 Chunk 才
   进入批量 Embedding；删除 Chunk 不进入新版索引；
6. 新版在 staging 状态完成解析、Chunk、FTS、Vector 和完整性检查；失败时旧版仍为 current；
7. 发布事务一次性把新版设为 current、旧版设为 retired，普通检索只查询 current；
8. 历史报告和 EvidenceItem 继续引用旧版 Chunk，不能因换版失去可追溯性；
9. 改变 Embedding 模型只重建 Embedding profile，不应伪装成企业文档内容发生了新版本；
10. 增量更新评测同时记录全量结果一致性、Embedding 复用率、更新时间、失败回滚和检索切换
    原子性，不能只记录“少算了多少向量”。

### 制度文档变更示例

假设《设备停机处理制度》只修改第三章并新增一个审批条件：

1. 管理员必须从原逻辑文档执行“上传新版本”，不能只凭相同文件名或标题自动覆盖；
2. 系统为原文件创建新的不可变对象并对新版执行完整解析，避免 Office/PDF 结构变化被局部差分遗漏；
3. Chunk 指纹由稳定章节路径、元素类型、归一化内容和分块策略版本组成；完全未变化的 Chunk 可
   复用 Embedding，第三章修改块和新增块重新计算，删除块不进入新版索引；
4. 不能用“语义相似”替代严格内容比较来复用向量：文字发生变化就必须重算，避免制度关键字或
   数值变化后仍命中旧语义表示；
5. 新版在 staging 中完成全部索引和质量检查后，事务性切换为 current；切换前线上检索仍使用
   旧版，不允许新旧 Chunk 混合组成一次回答；
6. 当前问答默认只检索 current 版本，历史报告继续引用旧版本和旧 Chunk。是否首版支持按生效
   日期查询历史制度，已决定不纳入首版。

### 增量更新的准确口径

本方案是“版本化全量快照 + 增量计算/增量索引”，需要区分四个层次：

1. 来源检测是增量的：使用稳定 `document_id`、来源 object version/ETag、SHA-256 和更新时间判断
   文件是否变化；未变化文件整条跳过。
2. 文档解析按格式选择：Markdown 等结构化文本可以按元素优化，但 PDF、DOCX、PPTX 等二进制
   文档变化后默认完整重解析。局部修改可能改变分页、标题层级、表格和后续 Chunk 边界，强行只
   解析局部会牺牲正确性。
3. 计算和索引是 Chunk 级增量的：新版解析后按规范化内容哈希与上一版或 Embedding Cache 比较；
   未变化内容复用 OCR/VLM/Embedding 结果，新增和修改内容重新计算，删除内容不进入新版索引。
4. 发布是版本化快照：新版所有 Chunk 先以 staging/version 维度写入，质量检查通过后只切换
   current 指针。对外表现为完整一致的新快照，内部并未重新调用全部昂贵模型。

主流 RAG 框架通常先实现文档级 `id + hash` 管理：未变化文档跳过，变化文档删除旧向量后重新
处理并 upsert，新数据源缺失的文档则删除。MESGuard 在此基础上增加 Chunk 内容哈希缓存和版本
发布：比默认的“变化文档全部重算”更细，同时满足历史报告继续引用旧 Chunk 的审计要求。

首版不采用 current Chunk 原地 update/delete。原地更新虽然减少历史 Chunk 存储，但会造成更新
窗口新旧内容混合、历史报告引用漂移和失败回滚困难。增量收益应通过“Embedding 实际重算比例、
缓存命中率、索引更新时间和发布一致性”衡量，不能用“新版是否创建完整 Chunk 行”来判断。

### 文件安全威胁模型与首版边界

“管理员上传”只能降低恶意意图概率，不能消除管理员终端被感染、文件从外部取得或解析器被
攻击的风险。需要区分四类威胁：

1. 已知恶意软件：病毒、木马以及 Office 宏、PDF JavaScript、嵌入对象等主动内容；
2. 解析器攻击：畸形 PDF、Office、图片利用第三方解析库漏洞触发代码执行或崩溃；
3. 资源耗尽：ZIP/XML 解压炸弹、超大像素图片、异常页数、深层嵌套对象导致 CPU、内存或磁盘
   被耗尽；
4. RAG 内容攻击：文档内隐藏提示词、伪造制度或污染检索结果。这不属于传统杀毒软件能力，
   必须依靠来源审批、版本审计、检索引用和 Agent 指令/文档内容分离处理。

首版建议不把 ClamAV 作为 RAG 主链路的强制部署前置条件，但实现以下工程边界：

- 对象先进入隔离状态，完成文件签名/MIME 一致性、大小、页数、像素、解压后总量、宏/外链和
  超时限制后才允许解析；
- 解析进程使用最小权限、临时目录、资源上限和默认禁网，不能执行宏、脚本或嵌入对象；
- 预留 `MalwareScanner` 接口、`disabled|optional|required` 配置和扫描审计状态；
- `required` 下扫描失败、超时或扫描器不可用都保持 `quarantined`，禁止解析、预览和检索；
  `partial_ready` 只表示内容处理不完整，不能表示安全状态未知；
- 本地开发可使用 `disabled`，生产管理员知识上传至少 `optional`，普通用户 session 附件建议
  `required`；ClamAV 或企业扫描服务作为可插拔适配器后续接入。

### MinIO Versioning 与 Object Lock

- Bucket Versioning 会为同一个 object key 的覆盖和删除保留多个对象版本，并通过 version id
  读取；它解决对象层误覆盖/误删除，不等同于业务文档版本。
- Object Lock 是 WORM 保留策略：Governance 模式可由具备特殊权限的管理员绕过，Compliance
  模式在保留期结束前任何人都不能删除，Legal Hold 则持续到显式解除。
- 本设计已经为每个附件和文档版本生成唯一不可变 key，并由 PostgreSQL 管理业务版本、引用和
  清理状态；首版再开启 Bucket Versioning 会形成两套版本事实，Object Lock 还可能阻塞合法清理。
- 因此建议首版关闭两者，通过不可变 key、数据库审计和备份保证恢复；只有出现法务留存、WORM
  或防特权删除要求时，才在专用 Bucket 上评估启用 Object Lock。

## 第三轮：文件安全、ERP 附件与版本发布

1. 是否接受恶意文件扫描采用配置模式：`disabled|optional|required`；本地开发可 disabled，
   生产管理员知识上传至少 optional，普通用户 session 附件建议 required？
2. `required` 模式下扫描失败或扫描器不可用时对象保持 quarantined，禁止解析、预览和检索；
   这类状态不能记为 `partial_ready`。是否接受？
3. 首版是否只预留 `MalwareScanner` 接口和扫描状态，先实现确定性文件安全校验与解析隔离，
   ClamAV 容器作为后续可插拔部署，而不阻塞 RAG 主链路？
4. ERP SQL 当前只有附件元数据/object key，没有文件读取协议。是否接受定义
   `ExternalAttachmentReader.Open(...)` 端口，本地演示用 MinIO fixture 实现，真实 ERP 以后按
   共享目录、HTTP 或对象存储适配，而不是在 SQL Repository 中猜测下载方式？
5. 是否确认首版关闭 MinIO Bucket Versioning/Object Lock：每个业务版本使用唯一不可变 key；
   PostgreSQL 管逻辑版本，备份负责灾难恢复，未来明确有 WORM/法务保留要求时再启用 Object Lock？
6. 更新版本进入 `partial_ready` 时，若已有 current 版本，默认继续提供旧版检索并等待管理员
   决定是否强制发布 partial；若是首个版本，可允许带缺失声明发布。是否接受？
7. 新版本身份是否必须由管理员从原文档执行“上传新版本”，禁止系统仅凭文件名/标题/相似度
   自动判断两份文件属于同一逻辑文档？

### 第三轮答复与待确认项

- 文件安全采用“确定性限制和隔离先落地、扫描器可插拔”的范围，避免非核心部署依赖阻塞第三条
  简历主线，同时保留生产 session 附件使用 `required` 扫描的能力。此项已确认。
- `ExternalAttachmentReader` 与 MinIO 不是二选一。前者是“如何从附件拥有方读取字节流”的领域
  端口；已经确认当前 ERP 实际文件存于企业 MinIO，因此首个真实适配器就是 MinIO Reader，SQL
  中的 object key 只能在该适配器内部解析，不能暴露给模型或前端。
- 已确认 ERP 附件首次成为诊断任务或报告证据时固化到 MESGuard 私有 Bucket。同一 MinIO 实例且
  具备权限时优先服务端 CopyObject，不需要文件流经 Go 进程；不同实例时再使用受限流式
  GetObject -> PutObject。固化副本按证据保留策略只允许逻辑删除，保证上游覆盖、删除或权限变化
  后仍可复核历史报告。
- 更新版本不能在上传后立即替换旧版。系统应先完成解析、Chunk、FTS、Vector 和质量检查；只有
  `ready` 才自动发布。若新版为 `partial_ready`，系统不能判断旧制度是否仍有效，因此不自动切换，
  而是要求管理员在“继续旧版、暂停该文档检索、带缺失声明强制发布新版”中明确选择。
- 当前数据库已经实现 `knowledge_document_versions.version/status/is_current`、每文档唯一 current 和
  Chunk 版本外键，版本化不是从零新增。已确认首版只保留显式 current，不实现复杂的按日期历史检索。
  `created_at/completed_at` 只表示上传和解析时间；制度正文中的日期可以抽取为候选元数据，但不能
  自动决定生效版本。若未来需要按业务日期检索，应增加管理员确认的 `effective_from/effective_to`。
- 仍建议首版关闭 MinIO Bucket Versioning/Object Lock；增量更新采用管理员显式创建版本、完整
  重解析、Chunk 级 Embedding 复用、staging 校验和原子发布，不做文件二进制差分解析。

## 第四轮：解析 DAG、OCR/VLM 分流与格式语义

### 推荐解析 DAG

```text
来源对象固化
  -> 安全扫描与格式签名
  -> 原生解析/页面渲染
  -> 统一 DocumentElement 中间模型
  -> 逐页/逐元素质量评分
  -> 规则路由到 OCR、表格恢复或 VLM
  -> 文本清洗与元素关联
  -> 结构感知分块及硬上限兜底
  -> 增量 OCR/VLM/Embedding 缓存
  -> FTS/Vector staging 索引
  -> 完整性检查与 current 发布
```

Go 负责状态机、幂等、预算、质量评分和路由；解析器、OCR、VLM 通过配置化端口接入。智能分流
首版不依赖额外分类模型：优先使用 MIME、原生文本密度、乱码率、图片尺寸/占比、标题/图注、
表格结构和 OCR 置信度等确定性信号，只有规则不能确定或视觉语义确有价值时才调用 VLM。

页面级判断不是互斥分类。同一页可能同时含有可复制正文、扫描签章、软件截图、统计图和表格，
因此路由分两级：

```text
页面级：读取原生文本层、布局对象和渲染图，判断是否需要区域检测
  -> 文本区域：优先使用原生文本；乱码、缺字或扫描区域才 OCR
  -> 原生表格：恢复行列和表头；扫描表格先 OCR/表格结构恢复
  -> 软件截图：OCR 提取错误码、按钮和状态；必要时 VLM 补充界面关系
  -> 图表/流程图/架构图：VLM 生成结构化描述，OCR 只作为其中的文字证据
  -> 装饰图片：跳过检索，但保留来源和忽略原因
```

多个处理器可能覆盖同一区域，合并阶段必须使用 bounding box 重叠、文本相似度和来源优先级去重：
原生可靠文本优先于 OCR 重复文本，OCR 的精确文字优先于 VLM 自由描述，VLM 只补充视觉关系而
不能覆盖原始数值。最终 Chunk 可组合相邻正文、表格或图片描述，但保留元素级来源映射。

统一中间元素至少包含：元素 ID、页/工作表/幻灯片、顺序、类型、章节路径、bounding box、原生
文本、内容哈希、父子/相邻元素关系、解析器版本和质量警告。Chunk 是若干元素的检索投影，不是
唯一解析事实；否则换分块策略时必须重新调用 OCR/VLM，也无法稳定引用原页面区域。

### 第四轮结论

- 已确认解析目标、云端 OCR/VLM 可配置路径、元素级混合分流、VLM 初始预算、Office/PDF 范围、
  `partial_ready` 质量门禁和 MinIO JSON artifact 持久化方案。
- 混合页面以区域/元素为单位路由，不做整页互斥三分类；OCR 与 VLM 是互补能力，OCR 负责可见
  文字，VLM 负责图形关系和界面语义。
- `30` 个视觉元素只是初始工程保护值，不是简历指标；必须用固定混合文档集测出覆盖率、调用率、
  延迟和成本后才能调整或对外说明。

## 后续 grilling 轮次

第一轮收敛后继续逐轮确认：

1. MinIO 对象模型、上传协议、幂等、引用和清理；
2. 解析 DAG、格式探测、文本/表格/图片分流及失败降级；
3. 语义分块、硬上限兜底、父子块和表格块策略；
4. Embedding 模型、维度、批处理、版本化和 pgvector 索引；
5. Advanced RAG 的 Query、Retrieval、Post-retrieval 三阶段增强；
6. Agentic RAG Tool/Skill、停止条件、预算和 EvidenceItem 契约；
7. 吞吐量、Recall@K、MRR、nDCG、忠实度、延迟和成本评测；
8. 失败恢复、可观测性、安全、容量和运维边界。

## 第五轮：语义切割、硬上限与父子 Chunk

### 推荐分块流水线

```text
DocumentElement
  -> 按标题层级、页/Sheet/Slide 和元素类型形成结构块
  -> 表格、图片描述、错误栈等不可随意拆分的原子块单独处理
  -> 短小同章节段落合并到目标 Token 区间
  -> 仅对弱结构长正文执行语义边界检测
  -> 超长结果按句子/标点执行硬 Token 上限切分
  -> 生成 child chunk、parent context 和元素来源映射
```

语义切割不能替代结构切割。标题、页码、表头、列表和图片区域是可靠边界；对所有文档逐句做
Embedding 不但成本高，还可能把表格、错误栈和步骤列表切坏。语义边界检测只用于没有可靠标题、
连续长篇且多个主题混杂的正文，候选句向量需要批量调用并按内容哈希缓存。

“Chunk 数量兜底”和“大段落兜底”是两个不同限制：

- 单 Chunk 硬 Token 上限防止一大段内容直接进入上下文；
- 每文档最大 Chunk 数防止异常文档造成数据库、Embedding 和索引资源耗尽；
- 超过单块上限必须继续切分，超过文档总块数则整版失败或要求管理员调整，不能静默截断后发布。

### 第五轮结论

- 已确认 Token 目标和硬上限、选择性语义切割、父子 Chunk、受限 overlap、表格/图片原子块、
  单版本 Chunk 总量门禁和固定语料消融评测。
- 语义切割是结构分块后的可选增强，不是所有句子必经的云端模型步骤；父块只在召回后按需展开，
  不能把每个命中的 300-500 token child 无条件放大成 800-1500 token 上下文。
- 首个实现采用逻辑父级而不是新增父表：`document_version_id + section_path` 标识父级范围，检索和
  Rerank 仍针对 child，最终命中后才读取同章节相邻 Chunk。这样不增加 Embedding 数量，后续只有
  在多 Chunk 固定集证明局部窗口不足时才物化 Parent Chunk。
- 超过 10,000 Chunk 表示当前解析或容量策略不适合该文档，必须失败并允许管理员调整，不能用
  静默截断换取表面上的 `ready`。

## 决策记录

| 日期 | 决策 | 理由 | 反对意见/风险 | 状态 |
| --- | --- | --- | --- | --- |
| 2026-08-04 | 建立单一阶段设计文档并先进行 grilling | 防止跨轮次上下文丢失和实现目标漂移 | 设计未收敛前不能把待选方案写成完成项 | 已确认 |
| 2026-08-04 | Go 负责编排，允许外部解析/OCR/VLM 服务 | 保留 Go 后端主线，同时避免重写成熟底层算法 | 需要统一超时、熔断、脱敏和供应商审计 | 已确认 |
| 2026-08-04 | 默认无 GPU，OCR/VLM 支持 `partial_ready` 降级和补处理 | 适应中低配置 Windows 服务器并避免单依赖拖垮全文档 | 状态机、缺失元素和重试幂等复杂度增加 | 已确认 |
| 2026-08-04 | 吞吐量使用串行 baseline 与有限并行 experiment 配对 | 使“提升 40%+”有可复现因果基线 | 数据集必须包含不同格式和大小，不能只测小文本 | 已确认 |
| 2026-08-04 | 个人知识库保留底层能力但暂不交付完整产品 | 第三条简历不依赖个人文档管理，缩短工作周期 | 后续开放时需要补管理 API、配额和删除体验 | 已确认 |
| 2026-08-04 | 附件与知识文档分离聚合并共享解析管线 | 支持临时证据、诊断补证和企业知识不同生命周期 | 需要显式关联表和统一解析任务契约 | 已确认 |
| 2026-08-04 | 文档更新采用不可变新版本、完整重解析、增量 Embedding 和原子发布 | 优先保证解析正确性，同时减少重复向量计算 | Chunk 边界变化会降低复用率，需要稳定分块指纹 | 已确认 |
| 2026-08-04 | 文件扫描使用三态策略并严格隔离 required 失败 | 区分本地开发、可降级部署和生产安全要求 | 生产 required 依赖可用的扫描服务 | 已确认 |
| 2026-08-04 | 首版实现安全端口、状态、确定性限制和解析隔离 | 保留工程安全边界且不让 ClamAV 部署阻塞简历主线 | 后续生产上线前仍需接入并演练扫描器 | 已确认 |
| 2026-08-04 | ERP 附件首次成为证据时固化到 MESGuard Bucket | 当前 SQL Server 只保存引用，固化可防止上游覆盖、删除或权限变化导致证据漂移 | 增加对象复制、幂等、存储和清理成本 | 已确认 |
| 2026-08-04 | 增量口径采用全量解析快照与 Chunk 级增量计算/索引 | PDF/Office 完整重解析保证结构正确，哈希缓存减少昂贵 OCR/VLM/Embedding | 需要稳定指纹、缓存和版本发布 | 已确认 |
| 2026-08-04 | 首版不按日期检索历史制度 | 控制版本能力复杂度，当前问答只使用管理员发布的 current | 未来合规场景需增加人工确认的生效区间 | 已确认 |
| 2026-08-04 | 混合页面采用页面粗分和元素细分路由 | 同页可同时存在文本、表格、截图和图表，整页三选一会重复处理或漏召回 | 需要区域检测和多来源去重 | 已确认 |
| 2026-08-04 | 解析产物保存为 MinIO 中的版本化 Element Artifact | 支持断点恢复、重分块和复核，同时避免首版建立庞大元素关系模型 | Artifact schema 必须版本化并校验哈希 | 已确认 |
| 2026-08-04 | 结构优先、选择性语义切割并使用 Token 双重门禁 | 避免对所有文本调用语义模型，同时防止单块过长和异常 Chunk 膨胀 | Token 阈值需要固定语料消融评测 | 已确认 |
| 2026-08-04 | Child 检索、Parent 按需展开 | 提高检索粒度，同时避免无条件放大模型上下文 | 需要稳定父子映射和展开预算 | 已确认 |
| 2026-08-04 | 单 active Embedding Profile 并通过固定集后切换 | 避免不同向量空间混检，模型升级不伪造文档换版 | 需要后台回填、回滚窗口和 profile 审计 | 已确认 |
| 2026-08-04 | 首版使用精确余弦与 RRF 双路融合 | 先建立带权限过滤的召回正确性基线，再由规模证据决定 HNSW | 精确检索在数据增长后可能不满足延迟目标 | 已确认 |
| 2026-08-04 | 检索权限过滤下推 SQL 且支持单路降级 | 防止未授权候选离开数据库，并区分空结果与系统故障 | 两路结果与降级状态需要统一响应契约 | 已确认 |
| 2026-08-04 | 诊断任务可由后端授权知识 Tool 且用户不选 Tool | 保留 Agent 自主调查与 TaskScope 授权边界 | 需要修改当前 diagnosis 禁止 knowledge 的代码约束 | 已确认 |
| 2026-08-04 | 高层 search_knowledge 封装 Advanced RAG | 防止 Agent 操纵底层通道、权限和候选预算 | Tool 内部需要可观测的阶段轨迹 | 已确认 |
| 2026-08-04 | 知识结论执行 Chunk 引用门禁并显示来源 | 防止生成内容无法复核 | partial/degraded 来源需要影响结论强度 | 已确认 |
| 2026-08-04 | Web Search 采用 Firecrawl 且公开证据独立治理 | 补充知识库没有的公开技术信息，不污染企业知识 | 需要脱敏、SSRF、来源评级和网页提示注入隔离 | 已确认 |
| 2026-08-04 | 吞吐主指标使用 cold 配对实验，warm incremental 单列 | 保证 40% 增幅可归因于流水线和批处理 | 结果不达标时必须调整实现或简历口径 | 已确认 |
| 2026-08-04 | LLM Judge 独立配置且只作辅助评分 | 避免生成模型自评和单一主观指标 | 需要冻结模型、Prompt 和保存原始 Observation | 已确认 |
| 2026-08-04 | 公开技术语料按内容家族切分并记录许可与派生链 | 扩充格式与规模且防止跨格式数据泄漏 | 下载前仍需逐项确认 LICENSE/NOTICE | 已确认 |
| 2026-08-04 | 入库任务复用 PostgreSQL/Outbox/RabbitMQ 可靠异步基础 | 与诊断任务保持一致的事实源、租约和恢复语义 | 需要独立 Queue、状态和阶段 checkpoint | 已确认 |
| 2026-08-04 | 自有 MinIO Bucket 首版关闭 Versioning/Object Lock | 唯一不可变 key 与业务版本足够，避免双重版本事实 | 依赖备份和延迟 GC，未来 WORM 需求再评估 | 已确认 |
| 2026-08-04 | 自部署 Go+ONNX 版面路由，OCR/VLM 继续云端 | 在 Windows CPU 上低成本完成页面/区域分流，不引入完整 Python 解析运行时 | 需要打包 ONNX Runtime、固定模型权重并实现预后处理 | 已确认 |
| 2026-08-04 | Eino 只做 LayoutRouter 适配和 Callback，不拥有 Element 事实 | 保留框架观测能力，同时避免 Artifact 与 Eino schema 强耦合 | 需要维护一层薄适配器 | 已确认 |
| 2026-08-04 | Step 3.7 Flash 与 Qwen3-VL-Plus 做同图配对评测 | 厂商峰值 TPS 不能代表图片端到端吞吐和结构化输出质量 | 需要保存 profile、限流、成本和质量 Observation | 已确认 |

## 第九轮：吞吐量与 RAG 效果评测

### 文档处理吞吐量

简历“吞吐量提升 40%+”的实验单位不能只用文档数，因为 1 页 TXT 与 500 页扫描 PDF 不可比。
建议同时报告：

- `documents/min`：便于说明业务批次能力；
- `pages/min`：适用于 PDF/PPTX 和扫描件；
- `MiB/min`：反映对象读取和解析吞吐；
- `elements/s`、`chunks/s`：定位解析和索引阶段；
- 端到端 P50/P95、失败/partial 比例、CPU/内存峰值和云端调用/Token 成本。

固定 `rag-ingestion-v1` 建议至少 40 份可公开或合成的工业文档，覆盖 8 种格式和规模分层：
TXT/Markdown、原生 PDF、扫描 PDF、DOCX、XLSX、PPTX、PNG/JPEG；同时包含纯文本、表格、截图、
图表、混排页面和至少一批接近上限的大文档。数据集冻结 SHA-256、页数、元素类型和预期状态，
失败样本不能从结果中删除。

配对实验固定同一机器、数据库、对象、模型 profile、网络和 Worker 资源，每个 variant 预热后至少
重复 5 次，报告中位数与离散程度：

- baseline：单 Worker 串行执行下载、解析、OCR/VLM、分块、逐 Chunk Embedding、逐行写库；
- experiment：异步有界流水线、格式/页面有限并行、Embedding 批处理、数据库批量写入；
- cold：清空派生 Artifact/Embedding Cache，用于比较并发和批处理的实际贡献；
- warm incremental：同一批文档只修改部分内容，用于单独报告缓存命中和增量更新收益。

`40%+` 只能由 cold baseline 对 cold experiment 的端到端吞吐比较得出；warm cache 可以另报更高
收益，但不能混入主指标。还需通过逐项消融说明吞吐来源：仅流水线、+批量 Embedding、+批量写库、
+增量缓存，并验证吞吐提升没有提高失败率或降低解析完整性。

### 检索与回答效果

现有 `rag-retrieval-v1` 的 12 文档/24 查询和 FTS Recall@5 95.83%、MRR 0.9028 继续作为最小回归
集，但不能单独证明混合文档、向量、Rerank 和 Agentic RAG。新增固定评测至少分三层：

1. `rag-retrieval-v2`：扩大到至少 50 份文档、约 120 条查询，包含 literal、paraphrase、错误码、
   数值/否定条件、表格、图片、跨段和无答案样本；人工标注 relevant document/chunk。
2. 分类型 Retrieval：报告 overall 及 text/table/image/no-answer 的 Recall@5、MRR、nDCG@10、
   Context Precision，并对 FTS、Vector、RRF、Rerank 做配对消融。
3. Answer/Citation：每条问题标注关键事实和允许来源，评估答案正确率、引用召回/精度、引用定位
   正确率、忠实度、拒答准确率以及 partial/degraded 场景是否过度下结论。

`Recall@5 >= 90%` 必须明确是哪个数据集、哪个 relevant 粒度和哪个系统 variant。最终测试集在参数
冻结后只运行，不用于调参；Query 改写、RRF、Rerank 和 Chunk 阈值只能在 train/dev 上确定。

### 第九轮结论

- 已确认 40 份/8 类格式的入库集、50 份/约 120 查询的检索集、每 variant 至少 5 次、相同外部
  模型条件、cold/warm 分离、逐项吞吐消融、Chunk 主粒度和固定 train/dev/test。
- “吞吐提升 40%+”当前是待实验验证的验收目标，不是已完成事实。若稳定结果不足 40%，简历改为
  实测数值，或改写为“通过有界流水线、批量 Embedding 与批量写入降低 P95、提升处理吞吐”，
  不能混入增量缓存收益、挑最好一次或删除失败样本来保留原数字。
- 公开计算机行业技术文档可以进入评测集，工业文档由用户后续补充；数据集报告必须按内容类型
  分层，不能用大量同质公开手册掩盖表格、图片和工业混排样本不足。

### LLM Judge 辅助评分契约

已新增默认关闭的 `[models.judge]` 配置骨架，首个 Provider 使用百炼 DashScope 的 OpenAI 兼容
端点，Key 只从 `DASHSCOPE_API_KEY` 读取。模型、Prompt 文件、Prompt 版本、超时和输出上限均可
配置；`config/prompts/rag-judge.md` 固定 JSON 输出和 0-4 分评分锚点。当前只完成配置与契约，
Judge 客户端、评测命令和 Observation 持久化尚未实现。

Judge 输入固定为问题、answerable、人工黄金事实、允许来源、候选答案和实际引用证据。处理顺序为：

```text
确定性检查（引用存在/权限/定位/黄金事实匹配）
  -> 独立 Judge 辅助评估 correctness/faithfulness/citation/refusal
  -> 严格 JSON 解码和 0-4 语义校验
  -> 保存 request model、response model、promptVersion、prompt SHA-256 和原始评分
  -> 评测程序计算汇总，Judge 不直接决定简历指标
```

生成答案的 ChatModel 不能评自己的答案。Judge 超时、限流、格式错误或未配置时，该样本只缺少
辅助评分，人工黄金事实、Retrieval 和确定性 Citation 指标仍可完成；禁止把 Judge 失败样本从分母
删除。若配置使用会动态升级的模型别名，Observation 必须记录供应商响应中的实际模型标识；正式
test run 优先固定模型版本并冻结 Prompt SHA-256。

### 公开大型技术语料候选（2026-08-04）

以下只是获取候选，下载前仍需逐项确认 LICENSE、NOTICE、第三方图片和再分发条件。原始二进制不
直接进入 Git；评测数据目录保存 source manifest、SHA-256、获取时间和许可快照。

| 来源族 | 官方候选 | 原始形态与用途 | 许可门禁 |
| --- | --- | --- | --- |
| PostgreSQL | [PostgreSQL 18.4 Documentation](https://www.postgresql.org/files/documentation/pdf/18/postgresql-18-A4.pdf) | 超大型原生 PDF，覆盖标题、代码、表格、索引和跨章节查询 | 核对 PostgreSQL License 与文档内第三方声明 |
| NIST | [SP 800-53 Rev.5](https://nvlpubs.nist.gov/nistpubs/specialpublications/NIST.SP.800-53r5.pdf)、[SP 800-53A Rev.5](https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-53Ar5.pdf) | 数百页安全控制 PDF，适合长文档、编号、表格和引用定位 | 记录美国政府作品声明并检查第三方材料 |
| Microsoft Learn | [MicrosoftDocs/sql-docs](https://github.com/MicrosoftDocs/sql-docs) | 大量 Markdown、图片、表格和代码块，主题与现有 SQL Server 场景相关 | 以仓库 LICENSE、NOTICE 和单文件声明为准 |
| Kubernetes | [kubernetes/website](https://github.com/kubernetes/website) | 大型结构化 Markdown/HTML 文档树，适合层级分块和跨页检索 | 区分文档内容许可与仓库代码许可并保留署名 |
| RFC Editor | [RFC 9110 TXT](https://www.rfc-editor.org/rfc/rfc9110.txt) 等选定 RFC | TXT/HTML/XML 多形态标准文本，适合错误码、规范词和否定条件 | 按 IETF Trust Legal Provisions 核对复制与派生条件 |
| Go | [golang/go](https://github.com/golang/go) 的 `doc/`、规范和设计文档 | HTML/Markdown/TXT 与代码片段，适合技术问答和精确术语 | 固定 commit 并保留 BSD License、AUTHORS/PATENTS |
| OpenTelemetry | [opentelemetry.io](https://github.com/open-telemetry/opentelemetry.io) | 多语言 Markdown、配置片段和可观测性概念文档 | 固定 commit，核对仓库许可和第三方素材 |
| OWASP | [OWASP ASVS](https://github.com/OWASP/ASVS) | 安全要求文档及可生成 PDF/CSV 的结构化源，适合表格和编号查询 | 保留署名与相同方式共享要求，下载前核对 release 许可 |

2026-08-04 通过 HTTP Range 实测前三个 PDF 可访问：PostgreSQL 约 15.0 MiB、SP 800-53 约
5.8 MiB、SP 800-53A 约 7.1 MiB。文件大小只能证明容量覆盖，不能替代页数、元素类型和解析质量标注。

`rag-ingestion-v1` 不把 40 份理解为 40 个独立来源。推荐先获取 10-15 份许可明确、内容足够长的
原始文档，再从允许派生的内容生成 DOCX/XLSX/PPTX、扫描 PDF 和 PNG/JPEG 测试变体。每个派生件
记录 `parent_content_sha256`、`generator_version`、格式参数和预期解析元素；同一内容家族整体放入
train、dev 或 test 中的一个集合，避免模型通过另一格式见过答案。

## 第六轮：Embedding、pgvector 与混合召回

### 推荐索引模型

```text
current document version
  -> child chunk
     ├─ PostgreSQL tsvector / GIN
     └─ embedding profile -> pgvector

query
  -> 权限、scope、current、文档元数据过滤
  -> FTS top-N + Vector top-N
  -> RRF 融合与重复内容折叠
  -> 候选集交给 Rerank
```

Embedding 必须使用独立于 ChatModel 的配置和端口。一个 `EmbeddingProfile` 至少包含 provider、
model id、dimensions、distance metric、query/document input mode、归一化选项和配置版本。缓存键为
`profile fingerprint + normalized content SHA-256`，不能只按文本哈希复用不同模型的向量。

PostgreSQL 的 `vector(n)` 维度在建表时固定。切换到不同维度或不兼容模型时，创建新的 profile
表/分区和 staging 索引，后台回填完成并通过同一评测后再切换 active profile；不能原地混存，也
不能为了换模型伪造文档内容版本。

首版在固定语料和小数据量上使用精确余弦检索建立正确性基线。HNSW 仅在真实规模压测表明精确
检索不满足延迟目标后启用。pgvector 近似索引先扫描候选再应用过滤，权限/scope 过滤比例高时可能
降低有效 Recall，因此启用 HNSW 后必须单独评测过滤后 Recall，并按实际 pgvector 版本评估
iterative scan、分区或部分索引，不能仅报告未过滤性能。

### 第六轮结论

- 已确认 Embedding 独立配置和真实 POC、单 active profile、精确余弦基线、RRF 双路融合、SQL 内
  权限过滤、单路降级和严格内容哈希缓存。`rag-retrieval-v1` 固定集已完成 FTS/Vector/RRF
  配对运行：FTS Recall@5 为 95.83%，Vector 和 RRF 均为 100%；Vector MRR 为 1.0，RRF MRR
  为 0.9792。结果和 Token/耗时口径记录在 `docs/evaluations/rag-retrieval-v1.md`，不能把
  该小样本直接外推为生产效果。
- HNSW 是规模驱动的性能优化，不是首版正确性的前置条件；任何近似索引结果都必须和精确检索在
  同一带权限过滤的固定查询集上比较，不能只测未过滤的全库延迟。
- Embedding 模型升级与文档内容换版是两个正交维度，不能因为换模型而创建伪业务文档版本。

## 第七轮：Advanced RAG 与 Agentic RAG 控制边界

### 推荐在线检索流水线

```text
Agent / Knowledge QA 收到问题
  -> Pre-retrieval
     - 从对话中生成可独立理解的问题
     - 保留错误码、编号、数值、否定词和时间条件
     - 术语/缩写扩展，必要时拆为最多 3 个子查询
     - 服务端注入权限、session、current 和文档过滤
  -> Retrieval
     - FTS + Vector 双路召回
     - RRF 融合、内容哈希去重和 parent/child 折叠
  -> Post-retrieval
     - Rerank top candidates
     - 文档/章节多样性和上下文 Token 预算
     - 按需 parent 展开，保留完整 child 与引用定位
  -> Generation / Verification
     - 仅基于证据回答并绑定 chunk sourceRef
     - 校验引用存在、权限正确、内容未被模型改写
     - 证据不足时明确拒答或返回缺口
```

Advanced RAG 是一次 `search_knowledge` Tool 调用内部的确定性流水线。Agentic RAG 是上层控制器：
决定当前问题是否需要知识检索、第一次查询表达、是否根据证据缺口再检索一次以及何时停止。模型
不能直接选择 FTS/Vector 权重、绕过权限、指定 object key 或无限扩展上下文。

Tool 输入只允许业务查询和有限筛选，例如 `query`、可选 `documentIds`、`timeHint` 和
`maxResults`；用户身份、scope、session、active profile、候选规模上限和预算由服务端 TaskScope
注入。Tool 输出统一包含 chunks、引用定位、融合/重排分数、degraded/missingChannels、查询改写
摘要和是否仍缺证据，不向模型暴露原始向量或对象存储地址。

### 诊断任务与知识库授权

`NormalizeTaskRequestScope` 在任务创建时保留用户对 `code`/`sql` 的有限声明，然后始终追加 `knowledge` 并写入冻结的 `requestScope`。用户显式提交 `knowledge` 会被拒绝，这表示它是后端策略而不是前端开关。Worker 根据已持久化的能力构造 `TaskScope`，因此旧任务仍按当时快照执行。

Agent 的 `ticket-diagnosis` Skill 规定从工单开始，根据证据缺口在业务 SQL、企业知识和 GitHub 代码之间自主升级；Tool 的实际可见性仍同时受角色、数据源、依赖健康和只读政策约束。

### 第七轮结论

- 已确认 TaskScope 由后端授予知识能力、单一高层 Tool、规则保护后的 Query 改写、对话独立问题、
  配置化 Rerank、受预算约束的上下文、最多两轮 Agentic 重检索和事实引用门禁。
- 回答必须显式展示来源，而不是只在内部保留 `sourceRef`。知识来源显示文档标题、版本、页/Sheet/
  Slide、章节和片段定位；点击预览仍通过授权 API，不暴露 MinIO 地址。
- Web Search 作为公司知识缺失时的可选公开信息来源进入第八轮设计，不能自动写入企业知识库。

### Rerank 云服务建议（2026-08-04 查证）

首选阿里云百炼 `qwen3-rerank`：已完成真实供应商请求验证，请求契约包含 Query、Documents、`top_n`、以及结果的 `index/relevance_score`。
本次实际响应未返回可解析的 `usage.total_tokens`，适配器会透传该字段，但不用字符数估算并冒充为供应商用量。它和已接入的 `text-embedding-v4` 可减少供应商和密钥管理面。`gte-rerank` 系列已进入下线迁移，不作为新接入
首选。仍需使用 MESGuard 固定中文工业语料与以下备选做同口径对比：

- Jina `jina-reranker-v3.5`：多语言，单文档最大输入可配置到 8,192 tokens；
- Voyage `rerank-2.5` / `rerank-2.5-lite`：多语言、32K 上下文，分别偏质量和延迟；
- Cohere Rerank：100+ 商业语言并提供多种部署选择，适合作为企业采购备选。

POC 至少记录 nDCG@10、MRR、Recall@5、中文数值/否定条件排序准确率、P50/P95、超时率和每千次
查询成本。供应商公开描述或排行榜不能替代项目固定集结果。

### 当前实现检查点：M2-A8 Embedding、混合召回与 Rerank

M2-A8 已经完成后端基础链路：独立 `[models.embedding]` 配置、`EmbeddingProfile` 稳定指纹、
DashScope `text-embedding-v4` 适配器、`knowledge_embedding_profiles`/`knowledge_chunk_embeddings`
持久化、Worker 中的批量 Embedding、Chunk/向量同事务写入、active profile 防静默切换、精确
余弦 Vector Search 和 RRF 融合。检索查询仍在 SQL 内执行 current、ready、deleted、global/personal
scope 和内容哈希校验。

固定集观测还会区分文档 Embedding 与查询 Embedding 的调用数、供应商 Token 和耗时；评测命令不
从字符数推算 Token，只有显式提供价格参数时才写入估算成本。高层 `search_knowledge` Tool
已经接入知识问答 Runner，负责服务端封装召回和降级结果。可选 `[models.rerank]` 与 DashScope
`qwen3-rerank` HTTP 适配器已实现，默认关闭；启用后从最多 30 个候选重排到 Tool 请求的结果数，
`qwen3-rerank` HTTP 适配器已完成真实固定集评测：24/24 请求成功，Recall@5 为 24/24，MRR 为 0.9792，与当前 RRF 质量相同。本次供应商响应未返回可解析的 `total_tokens`，因此不估算或虚构成本；测试中 Rerank 平均约 81.9ms，独立运行的整体查询均值为 258.8ms，相对 RRF 文档中 211.3ms 的观测增加约 22.5%。这是两次运行的差异，不作严格因果结论。
剩余高层边界是 Web Search 和公开知识问答 API；逻辑 parent/child 上下文扩展、受控 Query Plan、
整 Chunk 压缩和 Evidence Gate 驱动二次检索已实现，但还没有在扩展固定集上证明答案质量净收益，逻辑
Parent 也不是物化 Parent 索引；
知识 Chunk 的引用门禁已落地，新建诊断任务已自动冻结 knowledge capability，不由前端或用户勾选。

### 当前实现检查点：M2-B1 逻辑 Parent 上下文扩展

`search_knowledge` 现在采用 small-to-big 顺序：FTS/Vector 召回 child，RRF 去重，配置启用时先完成
Rerank，再只围绕最终 top K 结果扩展上下文。PostgreSQL 在 current/ready/scope 权限条件内，按
同一 `document_version_id + section_path` 批量读取前后有界窗口；默认窗口为 1、每个逻辑父级的
邻接内容预算为 1800 rune。窗口和预算由 `[knowledge.retrieval]` 配置，不能由模型参数覆盖。

Tool 输出把命中结果和 `contextGroups` 分开。命中 child 的 ID、原文和 SHA-256 保持不变；每个邻接
Chunk 也携带自己的 ID、ordinal、页面、类型和内容哈希。Tool 和 Evidence 边界会再次校验父级所属
版本、章节、命中引用、顺序和哈希。扩展失败只增加 `missingChannels=context` 并保留原召回结果；
没有邻接 Chunk 不算故障。本检查点没有额外模型调用、Embedding 或 Rerank 费用。

当前父级由章节路径和邻接窗口重建，不是独立持久化/向量化的 Parent。扩展后执行确定性整 Chunk
压缩：查询词覆盖、protected signals、邻接距离和命中排名共同评分；先让高排名命中组有机会保留
一个候选，再在全局 `maxChunks/maxRunes/minScore` 内补齐。压缩不截断、不摘要，保留原文和
SHA-256；Tool/Evidence 边界会核对统计与实际返回 Chunk/rune。压缩失败显式标记
`missingChannels=context_compression` 并保留原上下文。

生产初值为最多 6 个邻接 Chunk、3000 rune、最低分 0.05，命中 child 不计入该预算。是否物化
Parent 必须由多段落、多 Chunk 答案集的 Context Recall/Precision、压缩率、延迟和上下文膨胀率决定。

### 当前实现检查点：M2-B1 Evidence Gate 驱动二次检索

现有 EvidenceOrchestrator 仍最多运行 2 轮，不新增平行 Agent 循环。第一轮报告只有在缺口包含
“至少一条可追溯证据”、sourceTool 未成功或 sourceRef 未绑定本次 EvidenceItem 时，第二轮才继续
暴露 `search_knowledge`；纯 JSON、结论/摘要字段或 confidence 格式问题会隐藏该 Tool，只修复报告。

第二轮通过 run-scoped Tool policy 把 `search_knowledge` 限制为最多一次；该限制先于总 Tool 预算，
但不会绕过原有 Tool/Token/时间上限。SourceRef 每次调用都会变化，因此运行后按
`documentVersionId + chunkId + contentSha256` 判定是否真正新增知识证据，输出并持久化
`agenticRetrievalAttempted`、`agenticRetrievalAddedEvidence` 和稳定 stop reason，同时追加脱敏
`agentic_retrieval` 调查步骤。第二轮无新增证据不会启动第三次知识检索。当前合同/单测已完成，
真实模型三 Case 固定集也已完成：证据缺口 Case 实际调用 `search_knowledge` 并新增稳定证据；纯格式
Case 隐藏该 Tool；首轮合法 Case 不产生第二轮。三 Case 的尝试期望、尝试 Precision/Recall、增量证据
期望和停止原因准确率均为 1.0，合计 16453 Token。该样本隔离验证控制面，不衡量通用答案质量；
检索失败、重复证据、模型不选择和重复运行稳定性仍待扩展。

### 当前实现检查点：M2-B1 受控 Query Plan

Query Rewrite 不直接替换用户原问题。服务端先确定性提取错误码、版本、编号、数值、时间和否定词
等 protected signals，再让可配置改写器返回结构化 `QueryPlan`：原 Query、面向 FTS 的 lexical query、
面向 Vector 的 semantic query，以及最多 2 个必要子查询。原 Query 始终保存在计划中，在 512 rune
FTS 预算内进入关键词通道，并始终进入 Vector 查询集合。lexical/semantic 改写必须保留全部 protected
signals，子查询可以聚焦部分信号但不能新增信号；空值、控制字符、超长、重复和不一致 Token usage
都会被拒绝并回退原 Query。单字“未”不作为信号，避免把“未来版本”误判为否定；使用“未能”、
“未完成”等明确短语。

多 Query 不能把预算乘到不可控：每次 Tool 调用仍共享一个候选上限、Embedding/延迟预算和去重池，
FTS/Vector 各自先按最佳 rank、原 Query 顺序、通道分数和 Chunk ID 合并，再进入统一 RRF、可选 Rerank
和逻辑 Parent 展开；不同 Query 的原始 `ts_rank`/相似度不直接横向比较。改写器内部超时、无效或不可用
只标记 `missingChannels=query_rewrite`，继续执行基础 FTS/Vector；只有调用方上下文真正取消才中止检索。

`[knowledge.retrieval.queryRewrite]` 通过 `modelProfile` 选择独立快速模型，并配置 Prompt 文件、Prompt
version、1-30 秒超时、最多 2 个子查询和输出 rune 上限，默认 `enabled=false`。当前
`qwen-rewrite` 使用 `qwen3.6-flash`、关闭 Thinking、`temperature=0`、3 秒、256 输出 Token，默认最多
1 个子查询；这些是待测候选，不是已证明的最优参数。Tool 输出记录完整 QueryPlan、
`disabled/accepted/provider_failed/
policy_rejected` 状态、Prompt version 和供应商 Token usage；严格 JSON 解码要求 `subqueries` 必须存在
且为数组。Prompt 位于 `config/prompts/query-rewrite.md`，修改内容时必须同步提升版本。

旧版复用主 StepFun Profile 的 smoke 共进行了三次短 Query 调用：10 秒预算出现内部超时；30 秒预算第一次返回后因
protected signals 不完整被门禁拒绝；将服务端提取的信号显式提供给模型后通过，测试总耗时观测约
17.6 秒。这只证明结构化契约和回退门禁可用，也说明延迟/稳定性不足以默认开启；没有
Recall/MRR/Context Precision 净收益证据。后续 paired 单 Case 又观测到 1152 Rewrite Token 和约
8.4 秒总延迟，根因是改写复用了主模型的 medium reasoning/4096 输出上限；这直接促成了独立小模型
Profile。`qwen-rewrite` 首次合同 smoke 约 1.14 秒返回，但因 Prompt v1 允许 2 个子查询而运行时只允许
1 个，被确定性门禁拒绝；Prompt v2 已把 `maxSubqueries` 作为运行时输入并保留响应后校验，尚未二次
调用。详细边界见 `docs/evaluations/query-rewrite-v1.md`。

### 当前实现检查点：M2-B1 Advanced RAG paired 评测合同

`AdvancedRetrievalEvaluationCase` 现在把人工黄金标签固定到文档键以及
`documentKey + chunk ordinal + content SHA-256`，Chunk 内容或分块顺序漂移会使 observation 校验失败，
不会静默沿用旧标签。每个 Case 必须且只能提供 baseline/experiment 一对 observation，并固定相同
Retriever、Embedding profile、Rerank profile 和 K；Query 轴记录 `original/rewrite`，上下文轴记录
`child/parent`，压缩轴固定 Parent 并记录 enabled、maxChunks、maxRunes 和 minScore，因此可以做
original-child、rewrite-child、original-parent、rewrite-parent 和 parent-compression 的受控对照。

离线汇总器 `cmd/mesguard-rag-paired-eval` 严格读取 JSONL，拒绝未知字段、混合 K、缺失 pair、重复
Chunk 位置、底层 profile 漂移以及同时改变两个实验轴的混杂 pair。每对实验只能按固定方向比较
`original -> rewrite`、`child -> parent` 或 `uncompressed parent -> bounded parent`。输出 Hit Rate@K、
Document Recall@K/MRR、Context Precision/Recall、查询放大倍数、压缩输入/输出 Chunk 与 rune、
省略数、压缩率、上下文 rune 变化率、延迟变化率、改写状态和供应商 Token。Hit Rate 表示每个问题是否至少
命中一个相关文档，Document Recall 则按该问题召回的相关文档比例计算，两者不混用。

领域层 `AdvancedRetrievalObserver` 已把两个真实 `SearchService` arm 的结果转换成严格 observation：
固定文档键映射，记录 Hit/邻接 Chunk 的 ordinal、SHA-256、rune 数，按启用通道统计 QueryPlan 查询数，
并保留降级通道、改写状态、Prompt/Provider/Model 元数据和供应商 Usage。Query Rewrite Provider 失败但
基础检索成功时保留回退结果；检索本身失败时写入 `search_failed/not_observed` 并按零质量进入汇总，
避免只统计成功请求；调用方取消或超时仍立即中止。Observer 只依赖稳定 Search 接口，不创建具体模型。

离线命令本身不连接数据库或模型。真实命令 `cmd/mesguard-rag-paired-observe` 已固定 4 份公开官方
文档、21 个 Chunk 和 5 个 Case，在 PostgreSQL 事务内构造临时知识数据、调用生产
`BuildKnowledgeSearchService`，结束后整体回滚；默认最多一个 Case，并要求显式
`-execute-provider`。Query Embedding、Rewrite 和 Rerank Token 分开记录。

首轮仅运行 `pool-limit-wait-risk`：逻辑 Parent 将 Context Recall 从 0.5 提高到 1.0，但上下文 rune
增加 108.97%、延迟增加 32.14%；Query Rewrite 没有改变质量指标，却消耗 1152 Rewrite Token、
使 Query Embedding 增加 157.14%，延迟增加 4434.33%。这些都是单 Case 观察，不是总体增幅；
完整边界见 `docs/evaluations/rag-advanced-v1.md`。

2026-08-07 已对 Compression 轴运行全部 5 Case。production 阈值为 6 Chunk/3000 rune/0.05，
13 个邻接 Chunk 全部保留，平均输入/输出均为 575.4 rune，Hit Rate、Document Recall、MRR 和
Context Precision/Recall 均不变。该结果验证链路但没有压缩收益；当前固定集缺少会触发生产阈值的
长 Parent 压力样本，不能宣称 Token 降低，也不能为追求降幅直接调低生产阈值。

随后新增独立 `rag-compression-pressure-v1`，固定 PostgreSQL 官方 Advisory Locks/Deadlocks 文档和
一个 `K=6` 多事实长章节 Case，不把压力 Query 混入通用质量均值。真实 RRF pair 在不改 production
阈值时连续三次将 7 个邻接 Chunk 压到 6 个、1507 rune 压到 1438 rune（-4.58%），Gold Context
Recall 保持 1.0。命令的 `-require-compression-acceptance` 会在零省略或黄金召回下降时失败。该结果
证明阈值能真实触发和证据哈希保持，不是总体 Token 降幅；仍需扩展多来源压力 Case。

`agentic-retrieval-v1` 进一步用固定首轮状态和固定 KnowledgeSearcher 隔离验证上层控制器。真实
`step-3.7-flash` 运行三 Case：证据缺口路径调用一次知识 Tool 并新增证据，格式修复路径不暴露知识
Tool，首轮通过路径不调用模型；停止原因分别为 `new_evidence_added/not_eligible/not_needed`，总计
16453 Token。一次 8000 Token 预跑还暴露了 Provider Usage 在响应完成后结算、单次响应可能越过总预算
的边界，因此评测默认按生产值 16000/Case 运行。该结果不替代答案/引用质量评测。

上线前固定集至少新增三类对照：口语/省略/多轮指代的可独立问题改写，错误码/版本/否定条件保真，
以及一个问题需要两个不同 Chunk 才能回答的多跳样本。比较 original-only 与 rewrite 的 Recall@K、MRR、
nDCG@10、Context Precision、查询放大倍数、P50/P95、Token 和费用；没有净收益时保持默认关闭。

## 第八轮：Web Search 公开技术调查

### 角色与升级路径

Web Search 是独立只读 Tool，不并入企业知识索引，也不是默认每问必搜：

```text
用户问题 / 诊断证据缺口
  -> 企业知识 search_knowledge（制度、SOP、内部产品事实优先）
  -> 若问题是公开技术事实，且内部知识无结果/过时/明确要求最新资料
  -> 生成脱敏后的 public query
  -> web_search 获取标题、URL、摘要、来源域和时间
  -> fetch_public_page 只读取 Agent 选中的少量页面
  -> 内容清洗、提示注入隔离、来源评级和去重
  -> 返回带 URL、标题、发布/更新时间和 fetched_at 的 WebEvidence
```

技术问题优先官方文档、标准组织、产品公告和上游仓库；Stack Overflow、博客和论坛只能作为辅助
线索。内部制度、当前数据库状态、工单事实和公司私有代码不能由公网内容替代。联网前必须移除
工单号、客户/员工信息、日志原文、数据库数据、内部主机名、私有仓库路径和未公开产品细节。

Web 页面是不可信数据。抓取内容不能成为系统指令，不能触发 Tool、下载/执行脚本或改变权限；
抓取器只接受 HTTP/HTTPS，禁止 localhost、内网/保留地址、重定向到私网、任意文件协议和超大
响应，防止 SSRF。模型不能自由构造 `fetch` 访问任意 URL，URL 必须来自受控搜索结果或服务端
允许域名。

### 第八轮结论

- 已确认 Web Search 的后端授权和自动升级条件、Firecrawl 首个适配器、来源分级、调用预算、引用
  格式、Evidence 快照边界和故障降级策略。
- 已实现 Web Search 的公网 Query 出口策略和 `[webSearch.redaction]` 配置：输入先执行字段最小化，
  再移除邮箱、电话、身份号、内网地址、内部主机/路径、URL、业务编号、哈希和管理员/任务动态词典；
  Bearer Token、API Key、密码、连接串、私钥、JWT 等凭证，以及 SQL、日志、堆栈、JSON 等结构化
  私有内容直接拒绝外发。脱敏后技术信息不足也拒绝，不把“只剩空壳”的 Query 发给供应商。
- `PublicQuery` 只能通过策略构造，命中记录只保留类别和数量，不保留敏感原值。当前工单动态词典
  来自客户、工单、生产标识、数据库别名和附件标识；公开产品/模块术语不自动删除，内部产品名由
  管理员通过 `sensitiveTermsEnv` 配置。规则、词典和最小化是安全边界，LLM 改写、NER 或云 DLP
  以后只能作为增强，不能绕开拒绝策略。
- `[webSearch]` 当前启用且 provider 为 `firecrawl`、base URL 为 `https://api.firecrawl.dev`，密钥只
  通过 `FIRECRAWL_API_KEY` 读取；密钥缺失、认证失败或 Provider 不可用时不注册 Web Tool，其他
  诊断能力继续运行。新诊断任务由后端自动冻结 `web_search` capability，前端不提供 Tool 开关。
- Firecrawl `/v2/search` 和 `/v2/scrape` Client、`web_search`/`fetch_public_page` Tool、Run 级
  2 Search/3 Fetch 预算和 `web` Evidence 已实现。Search 结果生成同 Run 随机 `resultId`，
  Fetch 不接受任意 URL；重复 Fetch 复用快照，不重复消费 Provider。
- URL Gate 在 Search 候选和 Scrape 最终报告 URL 两处校验协议、凭证、端口、域名解析及公网 IP，
  拒绝 localhost、私网、保留地址和混合 DNS；响应只接受 JSON、最大 2 MiB，正文最大 20,000
  字符并记录截断。Firecrawl 内部不可见的中间重定向仍依赖其服务端 SSRF 防护，不能描述为
  MESGuard 完整观察了每一跳。
- 网页以 `onlyMainContent` Markdown、控制字符清洗、`untrustedContent=true` 和系统级“只作数据”
  约束进入模型，不能授权 Tool 或改变 TaskScope。来源等级由配置化域名表确定，未命中一律保守为
  C；C 级来源不能独立支撑 conclusive 诊断。离线合同测试已完成，真实公网 smoke 尚未产生费用。

## 第十轮：失败恢复、可观测性与实施顺序

### 推荐任务状态与恢复模型

PostgreSQL 继续作为任务和发布状态的事实源，RabbitMQ 只负责异步派发和唤醒。一个不可变
`document_version` 对应一个入库任务，阶段结果通过内容哈希和 artifact 引用建立 checkpoint：

```text
uploaded -> quarantined/scanning -> parsing -> chunking -> indexing
  -> ready -> published(current)
  -> partial_ready -> waiting_retry/manual_publish
  -> failed/cancelled
```

Worker 领取任务后持有可续租 lease，并记录 stage、attempt、heartbeat 和 progress。进程崩溃或 lease
过期后，恢复 Worker 从最后一个已校验 checkpoint 继续；每个阶段必须以
`document_version_id + stage + input_fingerprint` 幂等，不能仅依赖消息去重。上传原文、元素 artifact、
Chunk 快照和 Embedding 缓存的生命周期不同，清理器只能删除数据库确认无引用且超过保留期的对象。

### 第十轮结论

- 已确认 PostgreSQL 事实源、RabbitMQ 派发、lease/heartbeat、阶段 checkpoint 和最多 3 次临时错误
  重试；永久输入错误不自动重试，必须管理员修正输入后创建新版本。
- 已确认 `partial_ready` 永不自动替换 current，但管理员可在完整缺失声明和审计记录下强制发布；
  取消采用协作式取消，已发布版本只能被新版本 supersede 或执行受审计删除。
- 已确认逻辑删除和延迟 GC：被任务、报告或 Evidence 引用的对象禁止直接物理删除；无引用对象默认
  30 天清理，上传后数据库事务失败的孤儿对象立即做有界补偿删除，补偿失败进入后续 GC 审计。
- 已确认按文档/OCR/VLM/Embedding 分资源设置有界并发，并统一记录 trace、任务、版本、阶段、
  attempt、队列延迟、P50/P95、重试/失败、缓存命中和外部调用成本；日志禁止输出敏感原文和凭据。
- 已确认知识文档属于不可信数据，只能作为引用证据进入模型，不能授权 Tool、改变 TaskScope 或成为
  系统指令；Web Search 排在企业知识主链路之后，不抢占第三条简历的关键路径。
- 后端实施顺序已冻结为：对象存储与版本引用 -> 入库任务/Outbox/恢复 -> 确定性多格式解析与
  artifact -> 分块/FTS -> Embedding/混合检索/Rerank -> `search_knowledge` -> 固定集评测。

### 已实现检查点：M2-A2 对象存储与入库任务骨架

2026-08-04 已完成第一批后端基础设施，范围严格止于“可恢复入库前置条件”：

1. 新增可降级 `[minio]` 配置与本地 Compose 服务，使用 `mesguard-attachments`、
   `mesguard-knowledge-sources` 两个私有 Bucket；50 MiB 上限、凭据环境变量、连接超时和自动建桶
   都由配置控制。MinIO 启动失败不阻止 PostgreSQL 核心 API，后续上传会重新尝试初始化。
2. 新增不暴露 S3 类型的 `objectstore.Store` 端口和 MinIO 适配器。对象键由服务端 UUID 生成，原始
   文件名只保存在元数据；流式 `PutObject` 同时计算 SHA-256，使用 `If-None-Match: *` 防止覆盖，
   返回逻辑 Bucket、object key、可选 VersionID、ETag、大小和媒体类型。
3. 自有 Bucket 首版按既有决策不强制启用 Bucket Versioning/Object Lock；不可变性由随机唯一键、
   写一次条件和 PostgreSQL 业务版本保证。若外部 S3/MinIO 已启用版本控制，返回的 VersionID 会
   一并固化并用于精确删除，不能把对象层版本替代业务文档版本。
4. `00012`/`00013` 为知识版本增加不可变原文引用、`pipeline_version` 和完整 staging/终态；新增
   `knowledge_ingestion_tasks/events`，保存 stage、attempt、lease、heartbeat、checkpoint、进度、
   取消、错误与时间。该检查点只预留 `idempotency_key/request_fingerprint` 约束；HTTP 重放语义已在
   后续 M2-A3 补齐。
5. `QueueVersion` 在一个 PostgreSQL 事务内创建 queued 版本、pending 入库任务、首个事件和
   `knowledge.ingest` Outbox；queued 版本不会提前成为 current。上传成功但事务失败时，应用服务
   使用独立 5 秒上下文补偿删除本次唯一对象。
6. RabbitMQ Publisher 声明持久化 `mesguard.knowledge.ingest` Queue 并路由
   `knowledge.ingest` Outbox；严格消息契约校验 AMQP 属性、信封和 task/version ID。M2-A4 已接入
   真实 Consumer，消息不再只停留在 Queue 中。

M2-A2 当时不包含管理员上传 HTTP API、附件表、文件签名/杀毒、解析 Worker、PDF/Office/OCR/VLM、
Element Artifact、Embedding 或在线 `search_knowledge`；其中上传 API、首层格式签名和 Worker 控制面
由 M2-A3 补齐，文本执行链和 Artifact 由 M2-A4 补齐，确定性 PDF/Office Parser 由 M2-A5 补齐。
在 M2-A2 检查点，附件、杀毒/OCR/VLM、Embedding 和在线 `search_knowledge` 仍未实现；因此该
检查点的吞吐与最终 Recall 指标仍然是待评测目标。

### 已实现检查点：M2-A3 管理员上传与可恢复 Worker 控制面

2026-08-04 已继续完成“上传可操作、执行可恢复”的后端控制面：

1. 新增管理员 `POST /api/v1/admin/knowledge-documents` 与
   `POST /api/v1/admin/knowledge-documents/{documentId}/versions`。首版使用有界流式 multipart 暂存，
   单文件上限由 `[knowledge].maxUploadBytes` 配置且不能超过 MinIO 上限；临时文件在请求结束后删除，
   不把 50 MiB 原文整体读入 Go 内存。
2. 上传格式限制为 UTF-8 TXT/Markdown、PDF、DOCX、XLSX、PPTX、PNG/JPEG；首层校验文本编码、
   PDF/图片魔数以及 Office ZIP 主结构和加密标记。该校验不是杀毒软件，也不能替代后续解析沙箱、
   解压规模、页数/像素和宏/外链限制。
3. `Idempotency-Key` 当前要求 UUID。请求指纹覆盖操作、已有 document id、标题、原文件名、规范媒体
   类型、大小、原文 SHA-256、pipeline version 和 retry budget；同键同指纹在上传 MinIO 前重放原
   task，同键不同指纹返回 `40911`，并发重复由数据库唯一约束和冲突后回读兜底。
4. 创建首版时 global 文档、queued version、pending task、首个 event 和 Outbox 在一个事务内完成，
   不再先创建可能孤立的逻辑文档；上传新版入口只允许已有 global 文档且不能修改标题。
5. 修复自有 Bucket 关闭 Versioning 时 MinIO 空 VersionID 的落库语义：Repository 将空值规范化为
   SQL `NULL`，ObjectKey 与 ETag/SHA-256 仍提供不可变引用。
6. 新增管理员任务查询和幂等协作式取消 API。查询只返回 stage/progress/attempt/错误和时间，不能
   暴露 checkpoint 原文或 ObjectKey；取消提交 `cancel_requested` 和追加事件，运行 Worker 通过
   heartbeat 观察并停止，服务端不强杀解析进程。
7. `knowledgeworker.Worker` 和 PostgreSQL Repository 已实现严格消息校验、claim、lease/heartbeat、
   attempt fencing、checkpoint、30 秒/2 分钟/10 分钟退避、临时/永久错误、重试耗尽和取消终态。
   过期 lease 可接管，旧 owner/attempt 无法再写 checkpoint 或终态。
8. 完整 `ready` 在 Worker 终态事务内按版本号原子切换 current；较旧版本晚完成不会覆盖较新 current。
   `partial_ready` 保持非 current，继续等待管理员审计决策。

真实 PostgreSQL 集成测试已验证 claim 竞争、过期接管、旧 fencing token 失效、checkpoint、退避、
ready 发布、`partial_ready` 不发布以及取消；真实 MinIO 集成测试已验证无 VersionID 对象上传。

### 已实现检查点：M2-A4 TXT/Markdown 可运行入库闭环

2026-08-04 已完成第一条可运行解析链路，边界如下：

1. `objectstore.Store.Get` 和 MinIO 实现按不可变 `ObjectRef` 读取原文。MinIO `GetObject` 后立即
   `Stat`，校验可选 VersionID、Size 和 ETag，并由调用方关闭流；Executor 再以有界读取校验原文
   Size 与 SHA-256，任何不一致都作为永久输入错误处理。
2. `knowledgeparser.Router` 依赖最小 `Parse` 接口。当前只注册 UTF-8 TXT/Markdown Parser，拒绝 NUL、
   非法 UTF-8 和未实现媒体类型；PDF/Office 即使通过上传边界校验，也会稳定进入不支持格式错误，
   不能被误报为已解析。
3. Parser 产出 `DocumentElement`，保留 element index、页码、类型、章节路径、正文和 JSON metadata；
   `ChunkElements` 是独立检索投影。Element 表示解析事实，Chunk 表示可重建的检索结构，后续更换
   Chunk/Embedding 策略不需要重新解释原文。
4. 在 M2-A4 检查点，完整 Element 集合以 schema version 1 JSON Artifact 写入 MinIO 的逻辑
   `knowledge-artifact` 前缀；`00014` 将 Artifact Bucket/ObjectKey/VersionID/ETag/Size/SHA-256 写入
   `knowledge_document_versions`。原文和 Artifact 当前共用知识物理 Bucket，但 key 空间和逻辑权限
   边界分离。
5. PostgreSQL `SaveParsedResult` 在事务内先锁定仍有效的 `task_id + claim_owner + attempt_count + lease`，
   再替换该版本 Chunks、保存 Artifact 引用并追加 `ingestion_result_staged`。旧 Worker 即使晚到也不能
   覆盖新 Worker 的 Chunk、Artifact 或 current。
6. 独立 `mesguard-knowledge-worker` 进程只装配 PostgreSQL、RabbitMQ 和 MinIO，不加载 Redis、ERP
   SQL Server、模型或 GitHub MCP。Consumer 使用 `prefetch=1`、手动 ACK、持久化 retry/dead copy 和
   Publisher Confirm；只有副本确认后才 ACK 原消息。
7. Worker 只有在 fenced staging 成功后才记录 publishing checkpoint，并在终态事务中发布
   `ready/current`；queued/indexing 版本不会被检索。任务 stage 与版本 status 都会区分
   `publishing`，避免监控把制品发布误判为仍在索引。

真实 PostgreSQL 测试已验证 Artifact/Chunk staging、旧 fencing token 无法覆盖、发布后 FTS 可检索；
真实 RabbitMQ 测试已验证 retry/dead copy Confirm 后 ACK；真实 MinIO 测试已验证 Source round trip。
服务级 smoke 使用现有管理员上传 API 投递 Markdown，经 Outbox Relay 和 Consumer 后得到
`succeeded/completed`、`ready/current`、`markdown-elements-v1`、8 个 Chunk 与 Artifact SHA-256，
随后清理了测试对象和数据库事实。单个 smoke 不构成吞吐量或 Recall 指标。

M2-A4 收工时仍未实现 PDF/Office Parser、逐页文本/扫描/复杂图表路由、OCR/VLM、Embedding、
向量召回、混合融合和 Rerank；其中确定性 PDF/Office 解析由下方 M2-A5 接续完成。

### 已实现检查点：M2-A5 受资源约束的 PDF/Office 确定性解析

2026-08-04 已把四种确定性 Parser 接入 M2-A4 的同一发布链路：

1. PDF Parser 使用纯 Go 读取器逐页提取嵌入文字，每个文本页形成保留 `page_number` 的 Element；
   加密、损坏、无页面或完全没有可提取嵌入文字的 PDF 作为永久输入错误。空白/无文本页会记录
   `visualEnrichmentRequired`，留给后续 OCR/VLM，而不会伪造页面描述。
2. DOCX 按文档流提取 Heading 1-6、Title、正文段落和表格，章节路径随标题层级更新；XLSX 按
   workbook relationship 顺序读取 worksheet，把 shared string、inline string、数字、布尔值等
   单元格值投影为 Sheet 级表格 Element；PPTX 按 presentation relationship 顺序读取 slide，提取
   段落和表格并保留幻灯片页码。
3. OOXML 打开时拒绝无效 ZIP、危险或重复路径、加密条目和逃逸 package root 的 relationship；
   不跟随 `TargetMode=External`。`[Content_Types].xml` 和业务 XML 使用严格解码，图片只统计
   `visualAssetCount` 并标记待增强，当前不生成未经视觉模型验证的文本。
4. `[knowledge]` 配置限制 PDF 页数/XLSX Sheet 数/PPTX Slide 数、ZIP 条目数、累计展开字节、
   单 XML 字节、累计抽取字符、单 Sheet 行列数；Parser 最多产生 10,000 个 Element。资源超限
   使用稳定 `ErrResourceLimit` 并由 Executor 分类为永久失败，避免无意义重试。XLSX 在每行写入
   Sheet 输出缓冲区前消费字符预算，重复 shared-string 引用不能先放大完整 Sheet 再事后拒绝。
5. 当前 PDF 第三方读取库内部的流解压不能施加完全精确的进程内存硬上限。本阶段以 API 原文大小、
   页数、抽取输出预算和独立 Knowledge Worker 进程隔离降低风险；生产部署仍需容器/进程级内存和
   CPU 限额，不能把这些应用层预算描述为完整解析沙箱。
6. 服务级 smoke 通过真实管理员上传 API 投递最小 PDF、DOCX、XLSX、PPTX，经 Outbox Relay、
   RabbitMQ 和 Knowledge Worker 后全部得到 `succeeded/completed`、`ready/current`、对应 Parser
   版本、非空 Chunk 和 64 位 Artifact SHA-256；随后精确删除 8 个 MinIO 对象及测试数据库事实。

本检查点当时没有实现扫描 PDF、PNG/JPEG、Office 图片/图表提取和区域定位、OCR/VLM、XLSX
公式表达式、隐藏 Sheet/Slide 语义、PPTX 演讲者备注或 `partial_ready` 视觉补处理；这些事项
在 M2-A6 中按视觉资产和降级语义继续实现，M2-A5 的四个最小 fixture 仍只能证明确定性链路，
不能构成吞吐量提升指标。

### 已实现检查点：M2-A6 视觉资产与可配置 OCR/VLM 路由

2026-08-04 在不破坏 M2-A5 确定性文本事实的前提下接入视觉资产阶段：

1. PDF 无嵌入文本页产出 `document_page`，保留页码和源文件 SHA-256；PNG/JPEG 产出
   `source_image`；DOCX/XLSX/PPTX 从包内 `media` 目录提取支持的视觉文件。所有视觉资产先做
   文件签名、尺寸、SHA-256、单项/总量预算校验，原始字节只存在受限执行和模型请求内。
2. Office relationship 只允许解析到同一 package root。图片记录 `sourcePart`、
   `relationshipId`、媒体路径和页码；同一图片被多个 PPTX slide 引用时保留多个可追溯 occurrence。
   没有关系引用的孤立媒体仍写入 Artifact 审计记录，但路由为 `unreferenced_asset`，不调用模型。
3. 路由由后端确定：小图片 `decorative_small_image` 跳过；可识别但暂不支持模型输入的媒体
   `unsupported_media_type` 跳过；PDF 页面缺少嵌入文本时走 OCR；足够大的 PNG/JPEG 走 OCR+VLM。
   每个任务还有独立的 `maxVisualEnrichments` 上限，超限资产只记录 `budget_exceeded`。
4. Element Artifact 从 schema v1 升为 v2，记录视觉位置、媒体类型、尺寸、内容哈希、路由、
   状态、原因、provider/model 和输出 Element 索引，禁止写入原始图片字节。原生文本存在但视觉
   处理不可用时版本发布为 `partial_ready` 且 `is_current=false`；纯视觉文档没有可检索文本时
   返回永久 `invalid_ingestion_input`，不生成空 Chunk。
5. `[models.ocr]` 与 `[models.vision]` 独立配置 provider、模型、Prompt 文件、Prompt version、
   超时和输出预算。DashScope 适配器只接受严格单对象 JSON，拒绝未知字段、尾随内容、NUL 和
   超大输出；凭证只注入 Knowledge Worker。最小 PNG 已完成真实 Vision 链路烟测，隔离的
   `qwen-vl-ocr-latest` PNG 烟测也已完成并保存 provider/model/Prompt metadata；直接 PDF
   `file_url` 输入单独验证为当前 Eino OpenAI adapter 不支持，现归类为永久输入能力错误，
   不再进入无意义重试。不能声称 OCR 质量、扫描文档召回或吞吐提升。

已通过视觉 Parser/路由/Artifact 单测、全量 `go test ./...`、`go vet ./...`、关键包 race
测试、PostgreSQL/RabbitMQ/MinIO integration 测试和 `docker compose config --quiet`；烟测
对象、任务事实和临时文件已清理。M2-A6 已收口。当前基于 MIME、原生文本存在性、尺寸和引用
关系的 M2-A6 路由仍是粗粒度 fallback，不能描述为区域级智能分流。

### 实施中检查点：M2-A7 本地 ONNX 页面/区域路由

2026-08-05 开始实现以下边界：

1. 新增 Go `LayoutRouter` 领域端口、页面/区域输入输出、bounding box、置信度、稳定 reason code
   和模型追踪元数据；Eino Document Transformer 仅在外层适配并通过 Callback 采集观测。
2. 原生文本质量规则先执行；明显数字文档保留现有 Go 快速路径，扫描/混合/低置信度页面才渲染并
   调用本地 ONNX 版面模型。模型 Session 常驻，线程、页数、像素、超时和并发有界。
3. text 区域在原生文本不足时走云端 OCR，table 区域进入表格恢复，picture/diagram/chart/screenshot
   区域进入云端 VLM；先做区域识别和多来源去重，最后才创建检索 Chunk。
4. 首轮不部署完整 Docling，也不本地化 OCR/VLM。Docling 用作离线对照；布局模型必须先完成
   许可证、标签、权重哈希、Windows/Linux ONNX Runtime 和预后处理契约审核。
5. VLM 固定相同区域、Prompt、严格 JSON schema、超时和输出上限，比较 StepFun
   `step-3.7-flash` low reasoning 与当前 DashScope `qwen3-vl-plus`；模型选择使用成功区域成本和
   端到端质量/吞吐，不使用厂商峰值 TPS 作为项目结论。
6. 固定集至少覆盖原生文本、扫描文本、原生/扫描表格、截图、统计图、流程图和混合页。先验证
   路由 Macro-F1、高价值漏检、CPU/RAM 和云调用降幅，再进入 Embedding、混合召回和 Rerank。

当前已经固定 PP-DocLayout-M commit、23 类映射、`640x640` 预处理、opset 17 和转换后 SHA-256；
PDFium-WASM、ONNX Runtime 1.28.0、常驻 Session/取消/并发边界均已实现。`LayoutStage` 已在开关
启用时接入 Knowledge Worker Executor，actionable crop 转为 `layout_region` 并显式路由 OCR 或
OCR+VLM，整页资产标记 superseded 以避免重复调用；Artifact schema v5 持久化 bbox、置信度、
reason、模型/渲染器、crop 哈希和 provider Token usage。文档级区域数和 crop 总字节预算在云调用前抑制超限区域并保留
suppression reason。上游语义样例已检出 table/caption/text，但这只证明接线正确。
真实公开固定集 `layout-routing-public-v1` 已固定 7 份 PDF/DOCX 的来源、使用依据、大小和哈希，
其中 8 个 PDF 页面完成了页面/路由及 7 个高价值区域人工标注。PDFium-WASM 现在会在主 Go Parser
返回空文本时先计数、再受限提取原生文字，并与主 Parser 共用文档字符预算；低置信 text/caption/table
不再直接升级到 VLM。Windows 本地运行得到页面类型 Macro-F1 1.0000、可执行路由 Macro-F1 1.0000、
高价值漏检 0/7；相对“所有检测区域均送云端”的路由基线，云端区域规避率为 73.08%。本轮没有调用
云 OCR/VLM，该口径不等于真实 API 调用、Token 或成本降幅。超大扫描页现会自适应降低实际 DPI 并
记录请求/实际 DPI，不再因为像素超限导致整任务失败。20M/8M 像素成对运行中，8M 保持本小样本
路由结果不变并降低 P95/内存。一次严格受限的 USPTO 扫描文字页 OCR 对比显示 72 DPI 与 113 DPI
输出字符相似度 99.54%，72 DPI provider 延迟低约 30.6%，但单页不足以覆盖小字、扫描表格和退化
图像，因此生产配置暂不切换。跨类别重叠框使用小面积、近同框和置信差三重门槛仲裁，固定集只
移除了 NASA logo 的重复 picture，云端区域规避率 73.08% 提升到 74.03%，高价值漏检仍为 0/7。

`element-merge-v1` 已在 Chunk 前执行：同页完全归一化重复、被原生文本完整覆盖的 OCR、以及
至少 85% 包含的重叠 OCR 会从检索投影中抑制；结构化 table/原生 text 优先，VLM 描述不做模糊
语义去重。原始 Element、winner index 和 reason 全部保留在 Artifact v5，避免为了少量 Token
丢失可审计证据。

`vlm-quality-local-v1` 已把三个真实课件图示裁成 SHA 固定区域，并用相同 Prompt、严格 JSON、
超时和 2,048 输出-Token 上限比较 `qwen3-vl-plus` 与 `step-3.7-flash` low reasoning。两者最终
都完成 3/3；自动关系事实均为 8/9，人工完整正确均为 2/3。StepFun 在 1,024 Token 试跑时出现
一次空 final content，提升到 2,048 后恢复；Qwen 最终轮平均 5.35 秒、2,206 Token、三次约
0.00684 元，StepFun 平均 7.87 秒、4,740 Token且计入 Step Plan 套餐额度。该小样本只支持当前
继续使用 Qwen 的工程决策，不支持宣称通用 VLM 精度领先。

本地 ONNX 资产现通过独立 BuildKit context 进入可选 Knowledge Worker 镜像，默认后端构建不携带
模型和 Runtime。staging、Docker build 和 Worker 初始化分别校验字节长度/SHA；Linux x64 镜像
携带模型 README、ONNX Runtime license 和 third-party notices，并以非 root、只读根文件系统、
无 capabilities 运行。2 CPU/2 GiB、禁网的八页固定集保持路由质量，平均页耗时 1.18 秒、P95
2.59 秒、峰值 RSS 638.06 MiB。该数据来自 Docker Desktop Linux VM，只证明可部署性和资源量级，
不能替代目标服务器容量测试。

9 份真实课程 PPTX 共 752 页已形成纯本地 Parser 基线：9/9 成功，三轮中位数 9.55 MiB/s、378.72 页/s，
并暴露且修复了合法 OOXML 零字节目录项被误判为不安全路径的问题。另有 2 份课件的 8 页经渲染与
独立 OOXML 人工复核，21 个页序文字锚点、9 个 DrawingML 表格、15 个图片使用点和 14 个 distinct
slide relationship 在固定集上全部通过；同一 relationship 的重复 picture 使用按关系级去重。三轮
Windows ONNX 线程 A/B 中，1/2/4 intra-op 的中位平均页时延分别为 1457.09/1389.57/1283.88 ms，
但 2 线程 P95 最稳，默认值暂不调整。上述数据均不含上传、云识别、数据库或向量索引。默认开关仍关闭。
后续还需补图表、软件截图/错误态和扫描表格，并完成云增强后的合并质量证据。详细口径与
结果位于 `docs/evaluations/layout-routing-public-v1.md` 与
`docs/evaluations/knowledge-ingestion-quality-v1.md`，架构决策位于
`docs/decisions/003-local-onnx-layout-routing.md`。

### 实施中检查点：M2-C 入库吞吐基线

入库吞吐优化当前只改变持久化执行方式，不改变 Artifact、Chunk、Embedding profile 或发布事务
语义。`[knowledge].chunkWriteBatchSize` 默认 100；Repository 在同一 fenced transaction 中分别批量
写入 Chunk 和 pgvector，`batchSize=1` 保留逐行参考路径。Embedding 仍使用既有 batch/concurrency，
没有为了吞吐降低输出维度或跳过向量。

评测使用稳定 `formatClass` 区分原生 PDF、扫描 PDF、DOCX、XLSX、PPTX、PNG、JPEG 和文本，类别
进入 corpus fingerprint。最终 acceptance 需要至少 40 份真实文档、8 类格式、5 个 paired repetitions，
并要求成功/partial/失败集合、Element 和 Chunk 完整性不回退。单一 MIME 数量不再被当成格式覆盖。

首个真实 Worker-core pilot 使用 NIST IR 8108 的 27 页/32 Chunk 原生 PDF，覆盖 MinIO、Parser、
DashScope Embedding、Worker checkpoint、PostgreSQL Chunk/pgvector staging 与发布。串行参考和实验
分别耗时 6686 ms/1130 ms，Embedding 请求 32/4，Chunk 与向量 INSERT 批次 32/1，Token 都为
7904，结果都为相同的 partial/7 Element/32 Chunk。491.68% 是组合路径的单样本观测；文档并发在
单文档上没有作用，且未逐项消融 Embedding 与数据库 batching，也未覆盖 RabbitMQ、OCR/VLM 或
layout。因此评测汇总保持 `AcceptanceEligible=false` 和 `MeetsTarget=false`。详细命令、合同和边界见
`docs/evaluations/rag-ingestion-throughput-v1.md`。

数据库批写已进一步完成独立消融：两份 NIST PDF 和一份 Microsoft DOCX 共 743 Chunk，五轮只改变
`chunkWriteBatchSize=1/100`。`SaveParsedResult` 中位耗时从 1752 ms 降到 406 ms，配对 staging
吞吐变化中位数为 +319.21%，每轮 Chunk/向量 INSERT 批次从 743+743 降到 9+9，Provider 调用和
Token 都为 0。这证明减少数据库往返的独立收益，不代表全链路同幅提升；3 文档/2 类仍不满足验收。

扩充真实语料时发现 Microsoft DOCX 通过 `word/document.xml.rels` 合法引用根级 `customXml`。OOXML
解析器已从“必须停留在首个顶层目录”修正为“不得越过虚拟 ZIP 根”的逐段 URI path 解析；同包跨
顶层引用可用，真正的根逃逸和外部目标仍拒绝。该兼容性修复不计入吞吐增幅。

为在调用云模型前隔离坏文件和格式缺口，新增 provider-free corpus audit：不连接 MinIO、PostgreSQL
或模型，但真实执行生产 Parser 与 Chunking，并区分 `text_ready`、`text_ready_visual_pending`、
`visual_enrichment_required`、`parser_failed`。2026-08-07 的固定清单已达到 40 份公开文档并覆盖全部
8 类格式，共 162,852,270 bytes，产生 5,946 个原始 Element、5,854 个可检索 Element、12,864 Chunk、
139 个视觉候选，0 份解析失败；生产 `element-merge-v1` 在 Chunking 前抑制 92 个重复或非语义
Element。27 份文档可直接文本检索，10 份文本可检索且等待视觉增强，3 份必须进入视觉增强路径。
视觉字节只统计实际物化的图片 `Content`，PDF 页面引用不再按页重复累计整份源文件。文档和格式门槛
已经通过，但没有 5 个完整全链路 pair，因此结果仍为 `AcceptanceEligible=false`、`MeetsTarget=false`。

固定集准入会先执行上传大小、签名、Parser 和 Chunking。83.85 MB 的 NIST PDF 超过 50 MiB 上传
上限而拒绝；NIST AMS 100-32 虽只有 1.70 MB/50 页，但当前 Go PDF 库的页文本提取超过 40 秒仍不
响应取消，因此从正向吞吐集移出并替换为通过审计的 NIST AMS 100-17。该反例证明 `context.Context`
只能在页间检查，不能终止第三方库内部阻塞；后续需要进程级 Parser 隔离或等价的可终止边界。

语料可复现性不依赖人工记忆：严格 manifest 记录 `publisher/sourceUrl/downloadUrl/usageBasis` 与文件
身份，下载地址只允许无凭据 HTTPS；PowerShell fetch 脚本把目标限制在被 Git 忽略的 evaluation 根目录，
已有文件先校验，下载文件也必须在临时路径通过字节数和 SHA-256 后才替换正式文件。

真实 Worker-core 并发测试进一步修正了评测隔离：生产 `QueueVersion` 会创建 Outbox，若真实 Relay 与
评测内嵌 Worker 同时运行，二者会竞争任务租约。评测器现把“创建任务 + 删除评测专属 Outbox”放在
同一 PostgreSQL 事务内，因此仍保留生产任务事实，但不会触发 RabbitMQ。逐文档 status/action/reason
也写入 Observation。加入费用预检、0.05 元默认预算、半额 TPM/RPM 平滑和 429 熔断后，仅改变
文档并发 `1 -> 2` 的五轮复测中位耗时 `2124 -> 1450 ms`、吞吐增加 46.48%；整轮 80 次请求、
96,060 Token、约 0.04803 元，其余 Element、Chunk、Token 和写入批次完全一致。该结果支持受控
Worker-core 的 40%+ 口径，但双文档/双格式边界必须保留，不能作为 40 文档混合视觉验收指标。

40 文档零成本估算为 12,864 个 Chunk：逐 Chunk 参考路径需要 12,864 次 Embedding 请求，生产
batch=10 路径需要 1,306 次。因此全规模验收先选择只改变文档并发 `1 -> 2` 的 pair，确认真实
Provider 限流、Token、耗时和费用后再安排 5 个交替顺序 pair；不直接把高请求组合 baseline 当作
默认全规模验收路径。

首个 40 文档并发 pair 的并发 1 arm 完成 12,864 Chunk、1,306 请求和 2,585,532 Token；并发 2 arm
在后 11 份文档遇到百炼 `429 Throttling.AllocationQuota`，只完成 10,923 Chunk。评测器没有把更短
耗时当作收益，而是因 Element、Chunk 和失败集合回退给出 `IntegrityPreserved=false`。表面
+141.57% 明确排除。两组共发起 2,412 次 HTTP 请求并消耗 4,668,907 Token，约 2.3345 元；并发组
平均 571.6 RPM、1,076,707 TPM，对照百炼北京地域 1,800 RPM/1,200,000 TPM，根因是滚动 Token
窗口突发而非 Key 累计额度耗尽。

评测器因此新增三层成本边界：Provider 调用前本地复用生产 Parser、Element Merge 与 Chunking，
估算完整 pair 的请求、Token 和人民币费用；默认整条命令最多 0.05 元，并以 900 RPM/600,000 TPM
匀速调度；第一个 429 或实际 Token 越过预算即取消全组，不再继续产生无效费用。40 文档继续作为
零成本格式覆盖与 Parser/Chunk 审计集，完整 Provider 复跑必须先单独审核预估费用。Batch API 虽然
价格更低且不受同步限流，但异步语义不同，不进入同步延迟对照。
