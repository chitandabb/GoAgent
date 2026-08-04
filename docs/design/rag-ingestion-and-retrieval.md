# MESGuard 第三阶段：混合文档解析与 Agentic RAG

## 文档状态

- 状态：`已完成设计确认 / 分阶段实施中`
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
- PDF 页数/文本预算以及 OOXML 条目、展开大小、XML、工作表行列等资源边界；
- PDF/DOCX/XLSX/PPTX 真实服务链路 smoke，均验证 Parser 版本、Chunk 和 Artifact SHA-256。

当前尚未实现：

- 附件领域表以及附件上传/内容代理读取 API；
- 内容代理读取、孤儿清理和完整对象生命周期；
- 扫描 PDF/独立图片解析、Office 视觉元素提取、OCR、VLM 和智能分流；
- XLSX 公式表达式、隐藏 Sheet/Slide 语义和 PPTX 演讲者备注等丰富 Office 语义；
- Embedding、pgvector、混合融合、Query 改写和 Rerank；
- 面向 Agent 的知识检索 Tool/Skill 与知识问答运行链路；
- 文档处理吞吐量基线和最终第三条简历指标。

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
37. 诊断任务可由后端策略授予 `knowledge` capability，但前端和用户不选择 Tool；独立知识问答只
    获得 knowledge/attachment，不获得 SQL、GitHub 或工单能力。
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
  权限过滤、单路降级和严格内容哈希缓存。
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

### 当前代码冲突

当前 `TaskScope` 明确禁止 diagnosis 任务获得 `knowledge` capability，并规定 knowledge task 只能拥有
knowledge capability。这适合早期隔离测试，但不满足“诊断 Agent 根据证据缺口自主查询企业制度、
手册和历史知识”的目标。推荐保留 ToolCatalog 授权事实源，但由后端策略为诊断任务冻结
`case + 可选 sql/code/attachment/knowledge`，前端和普通用户不直接勾选 Tool；Agent 只能在获授权
能力中自主决策。独立知识问答仍只获得 knowledge/attachment，不获得 SQL 或代码能力。

### 第七轮结论

- 已确认 TaskScope 由后端授予知识能力、单一高层 Tool、规则保护后的 Query 改写、对话独立问题、
  配置化 Rerank、受预算约束的上下文、最多两轮 Agentic 重检索和事实引用门禁。
- 回答必须显式展示来源，而不是只在内部保留 `sourceRef`。知识来源显示文档标题、版本、页/Sheet/
  Slide、章节和片段定位；点击预览仍通过授权 API，不暴露 MinIO 地址。
- Web Search 作为公司知识缺失时的可选公开信息来源进入第八轮设计，不能自动写入企业知识库。

### Rerank 云服务建议（2026-08-04 查证）

首选阿里云百炼 `qwen3-rerank`：官方当前文档显示支持中文及 100+ 语言、单条 Query/Document
最多 4,000 tokens、最多 500 个候选，并返回 `usage.total_tokens`；它和拟用的
`text-embedding-v4` 可减少供应商和密钥管理面。`gte-rerank` 系列已进入下线迁移，不作为新接入
首选。仍需使用 MESGuard 固定中文工业语料与以下备选做同口径对比：

- Jina `jina-reranker-v3.5`：多语言，单文档最大输入可配置到 8,192 tokens；
- Voyage `rerank-2.5` / `rerank-2.5-lite`：多语言、32K 上下文，分别偏质量和延迟；
- Cohere Rerank：100+ 商业语言并提供多种部署选择，适合作为企业采购备选。

POC 至少记录 nDCG@10、MRR、Recall@5、中文数值/否定条件排序准确率、P50/P95、超时率和每千次
查询成本。供应商公开描述或排行榜不能替代项目固定集结果。

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
- 已新增可禁用 `[webSearch]` 配置骨架，默认 provider 为 `firecrawl`、base URL 为
  `https://api.firecrawl.dev`、密钥只通过 `FIRECRAWL_API_KEY` 读取；当前未实现客户端或 Tool，
  `enabled=false` 不改变运行行为。
- 网页抓取必须实施 SSRF 防护、重定向复验、响应大小/类型限制、脚本不执行和 Prompt Injection
  数据隔离。这些是 Web Tool 的实现门禁，不因供应商已抓取页面而省略。

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
附件、杀毒/OCR/VLM、Embedding 和在线 `search_knowledge` 仍未实现，因此简历第三条的吞吐与最终
Recall 指标仍然是待评测目标。

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
4. 完整 Element 集合以 schema version 1 JSON Artifact 写入 MinIO 的逻辑
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
   `ready/current`；queued/indexing 版本不会被检索。任务 stage 的 `publishing` 仍映射到粗粒度版本
   status `indexing`，需要展示发布进度时读取任务 stage。

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

本检查点没有实现扫描 PDF、PNG/JPEG、Office 图片/图表提取和区域定位、OCR/VLM、XLSX 公式
表达式、隐藏 Sheet/Slide 语义、PPTX 演讲者备注或 `partial_ready` 视觉补处理。这些属于下一切片，
不能由 `visualAssetCount` 冒充。四个最小 fixture 也只能证明链路正确，不能构成吞吐量提升指标。
