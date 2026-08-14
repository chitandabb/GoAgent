package main

import (
	"math/rand"

	"github.com/chitandabb/GoAgent/internal/semanticcache"
)

const semanticCacheDatasetSeed int64 = 20260814

type draftPair struct {
	anchor    string
	candidate string
	rationale string
}

func buildDraftDataset() semanticcache.EvaluationDataset {
	dataset := semanticcache.EvaluationDataset{Version: "semantic-cache-v1", Seed: semanticCacheDatasetSeed}
	appendCategory := func(category semanticcache.EvaluationCategory, proposed bool, calibration int, pairs []draftPair) {
		permutation := rand.New(rand.NewSource(semanticCacheDatasetSeed + int64(len(dataset.Pairs)))).Perm(len(pairs))
		calibrationSet := make(map[int]struct{}, calibration)
		for _, index := range permutation[:calibration] {
			calibrationSet[index] = struct{}{}
		}
		for index, pair := range pairs {
			split := semanticcache.EvaluationSplitHoldout
			if _, exists := calibrationSet[index]; exists {
				split = semanticcache.EvaluationSplitCalibration
			}
			dataset.Pairs = append(dataset.Pairs, semanticcache.EvaluationPair{
				ID: categoryPrefix(category) + "-" + twoDigits(index+1), Category: category, Split: split,
				AnchorQuestion: pair.anchor, CandidateQuestion: pair.candidate,
				ProposedReusable: proposed, Reviewed: false, Reusable: false, Rationale: pair.rationale,
			})
		}
	}
	appendCategory(semanticcache.EvaluationCategoryReusable, true, 27, reusableDraftPairs)
	appendCategory(semanticcache.EvaluationCategoryDifficultNegative, false, 27, difficultNegativeDraftPairs)
	appendCategory(semanticcache.EvaluationCategoryTemporal, false, 13, temporalDraftPairs)
	appendCategory(semanticcache.EvaluationCategoryContext, false, 13, contextDraftPairs)
	return dataset
}

func categoryPrefix(category semanticcache.EvaluationCategory) string {
	switch category {
	case semanticcache.EvaluationCategoryReusable:
		return "reuse"
	case semanticcache.EvaluationCategoryDifficultNegative:
		return "hardneg"
	case semanticcache.EvaluationCategoryTemporal:
		return "temporal"
	default:
		return "context"
	}
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

var reusableDraftPairs = []draftPair{
	{"Global 知识文档如何发布？", "怎样让全局知识库中的文档正式生效？", "发布流程的独立语义改写"},
	{"如何给已有知识文档发布新版本？", "公司制度变更后怎样更新原知识文档？", "同一逻辑文档的新版本流程"},
	{"新版本索引完成前旧知识版本还能检索吗？", "知识文档换版处理中，当前版本是否仍可搜索？", "换版期间可见性改写"},
	{"DOCX 文档进入知识库时如何解析？", "DOCX 格式的 Word 文件入库会经过哪些解析步骤？", "DOCX 与 Word 同义"},
	{"没有文本层的扫描版 PDF 如何提取文字？", "没有文本层的 PDF 会怎样识别内容？", "扫描 PDF 与无文本层 PDF 同义"},
	{"复杂图表页面如何进入 VLM 链路？", "含复杂图形和表格的页面什么时候调用 VLM 多模态模型？", "视觉复杂页路由改写"},
	{"父子索引在知识检索中有什么作用？", "为什么检索小块后还要回取父级上下文？", "父子索引作用改写"},
	{"知识检索为什么同时使用 FTS 和向量召回？", "混合检索为何要结合 FTS 关键词与语义搜索？", "双路召回改写"},
	{"Rerank 在 RAG 流程中负责什么？", "RAG 中的 Rerank 重排模型为什么放在候选召回之后？", "Rerank 职责改写"},
	{"知识问答的引用来源如何返回？", "回答公司知识问题时怎样标明证据出处？", "引用要求改写"},
	{"知识文档更新后答案缓存怎样失效？", "Global 知识变化时历史缓存答案如何被清除？", "Generation 失效语义改写"},
	{"L1 精确答案缓存如何匹配问题？", "L1 缓存用什么方式识别相同问题？", "L1 exact 机制改写"},
	{"L2 语义答案缓存如何匹配改写问题？", "L2 缓存怎样复用语义等价的问题？", "L2 semantic 机制改写"},
	{"答案缓存的 TTL 有什么作用？", "为什么知识问答缓存需要设置 TTL 过期时间？", "TTL 作用改写"},
	{"Personal 知识是否进入全局答案缓存？", "个人知识库问答会被跨用户复用吗？", "Personal 排除策略改写"},
	{"管理员和分析员的权限如何区分？", "系统怎样限制各角色可执行的操作？", "角色权限改写"},
	{"诊断任务如何取消？", "用户怎样终止仍在执行的诊断？", "任务取消命令改写"},
	{"SSE 断线后如何续传任务事件？", "SSE 诊断进度流重连时怎样补回遗漏事件？", "SSE replay 改写"},
	{"Outbox Relay 如何可靠投递任务事件？", "Outbox Relay 怎样在数据库事务完成后发送任务消息？", "Outbox 作用改写"},
	{"Worker 执行失败后如何重试？", "后台诊断任务失败时采用什么恢复策略？", "Worker 重试改写"},
	{"Text-to-SQL 如何保证只读？", "模型生成 SQL 后怎样阻止写操作？", "只读 SQL 防护改写"},
	{"QueryGuard 会检查哪些 SQL 风险？", "QueryGuard 作为 SQL 执行前安全门禁负责什么？", "QueryGuard 职责改写"},
	{"GitHub Code Search 返回 incomplete_results 时如何降级？", "GitHub Code Search 返回 incomplete_results 后系统怎么办？", "GitHub 搜索降级改写"},
	{"本地仓库缓存为什么使用浅克隆？", "代码检索降级链路为何只拉取仓库头部快照？", "Repo Cache 浅克隆改写"},
	{"聊天附件存储在哪里？", "会话上传的文件由哪个对象存储保存？", "附件 MinIO 存储改写"},
	{"知识文档上传如何限制文件大小？", "系统怎样限制入库文件的体积？", "上传资源限制改写"},
	{"Prompt 为什么要配置化？", "提示词从配置文件加载有什么好处？", "Prompt 配置化改写"},
	{"如何在 StepFun 和 DeepSeek 之间切换主聊天模型 Provider？", "主模型从 StepFun 换到 DeepSeek 需要改业务流程吗？", "模型 Provider 切换改写"},
	{"软阈值如何在不阻塞用户时触发会话摘要？", "长会话在不阻塞用户时如何提前压缩？", "异步软压缩改写"},
	{"硬阈值触发压缩时会怎样处理？", "上下文即将超窗时系统如何同步收缩历史？", "同步硬压缩改写"},
	{"摘要 throughSeq 表示什么？", "会话摘要记录的覆盖消息序号有什么含义？", "摘要边界改写"},
	{"TaskScope 如何限制 Agent Tool？", "TaskScope 在运行时怎样只向 Agent 暴露被授权的 Tool？", "工具授权改写"},
	{"Skill 渐进式加载如何控制上下文？", "Skill 按需加载怎样减少 System Prompt 内容？", "Skill 加载策略改写"},
	{"Evidence Gate 如何决定证据是否充分？", "Agent 在输出结论前怎样检查证据完整性？", "证据门禁改写"},
	{"Evidence Gate Early Exit 对 ReAct 有什么作用？", "Evidence Gate Early Exit 为什么能在证据充分时提前结束 ReAct？", "Early Exit 作用改写"},
	{"OpenTelemetry 在 Agent 链路中记录什么？", "OpenTelemetry 如何统一追踪 Agent 的模型、工具和检索调用？", "OTel 观测改写"},
	{"工具失败时如何记录降级？", "Agent 调用依赖失败后怎样留下可观测事件？", "Degradation Event 改写"},
	{"文档页面如何在文本、OCR 和 VLM 间分流？", "知识解析怎样判断页面应走原生文本、OCR 还是 VLM？", "页面路由改写"},
	{"表格在文档解析后如何保留结构？", "知识入库怎样避免把表格完全压平成无结构文本？", "表格结构恢复改写"},
	{"知识问答为什么只缓存最终答案？", "为何答案缓存范围排除诊断任务和 Web Search 结果？", "缓存适用范围改写"},
}

var difficultNegativeDraftPairs = []draftPair{
	{"设备点检周期是 30 天吗？", "设备点检周期是 60 天吗？", "数字约束不同"},
	{"MESGuard v2.1 如何升级？", "MESGuard v2.2 如何升级？", "版本不同"},
	{"2026-08-01 生效的制度是什么？", "2026-09-01 生效的制度是什么？", "日期不同"},
	{"管理员可以删除已发布制度吗？", "管理员不可以删除已发布制度吗？", "否定极性不同"},
	{"MESGuard 的文档发布流程是什么？", "GoChat 的文档发布流程是什么？", "系统实体不同"},
	{"为什么要发布知识文档？", "如何发布知识文档？", "原因与操作步骤意图不同"},
	{"Global 文档如何发布？", "Personal 文档如何发布？", "知识作用域不同"},
	{"文档上传成功是否等于发布完成？", "文档上传成功后如何发布？", "状态判断与后续操作不同"},
	{"如何删除未发布草稿？", "如何撤回当前已发布版本？", "草稿删除与线上撤回不同"},
	{"OCR 失败时是否调用 VLM？", "VLM 失败时是否调用 OCR？", "降级方向相反"},
	{"如何开启语义缓存？", "如何关闭语义缓存？", "启用与禁用相反"},
	{"缓存命中是否调用主模型？", "缓存未命中是否调用主模型？", "命中状态不同"},
	{"精确缓存使用问题哈希吗？", "语义缓存只使用问题哈希吗？", "L1 与 L2 机制不同"},
	{"知识更新是否递增 Generation？", "知识上传是否递增 Generation？", "发布与上传事件不同"},
	{"诊断任务能否使用知识库？", "知识问答能否创建诊断任务？", "任务类型与能力方向不同"},
	{"SQL 查询超时后是否重试？", "SQL 写入被拦截后是否重试？", "暂时故障与策略拒绝不同"},
	{"SSE 断线如何续传？", "WebSocket 消息如何续传？", "传输协议不同"},
	{"Outbox 消息何时标记已投递？", "诊断任务何时标记已完成？", "消息状态与业务状态不同"},
	{"Worker Lease 过期如何恢复？", "用户会话 Cookie 过期如何恢复？", "租约与认证会话不同"},
	{"GitHub 私有仓库如何授权读取？", "GitHub 公共网页如何匿名搜索？", "认证代码读取与公开搜索不同"},
	{"代码搜索需要当前分支内容吗？", "代码搜索需要历史删除文件吗？", "当前快照与历史检索不同"},
	{"MinIO 保存知识原文吗？", "PostgreSQL 保存知识原文全文吗？", "对象存储与元数据存储不同"},
	{"附件能否用于当前轮诊断？", "附件能否自动进入 Global 知识库？", "临时证据与知识发布不同"},
	{"DOCX 原生文本如何解析？", "扫描图片中的文字如何解析？", "原生解析与 OCR 不同"},
	{"复杂图表是否走 VLM？", "普通文本段落是否走 VLM？", "视觉复杂度不同"},
	{"Rerank 是召回模型吗？", "Embedding 是重排模型吗？", "检索阶段职责不同"},
	{"父块是否用于向量召回？", "子块是否用于最终上下文扩展？", "父子块用途相反"},
	{"FTS 失败后能否降级向量检索？", "向量检索失败后能否降级 FTS？", "降级方向不同"},
	{"Prompt Cache 依赖稳定前缀吗？", "答案缓存依赖稳定前缀吗？", "Provider Prompt Cache 与业务答案缓存不同"},
	{"软阈值压缩会阻塞用户吗？", "硬阈值压缩会阻塞当前请求吗？", "异步与同步行为不同"},
	{"摘要覆盖到消息 100 吗？", "摘要覆盖到消息 120 吗？", "序号边界不同"},
	{"TaskScope 按能力授权 Tool 吗？", "Skill 文本会绕过 TaskScope 授权吗？", "授权来源与绕过问题不同"},
	{"工具调用失败能否返回降级答案？", "结构化 JSON 校验失败能否静默接受？", "可降级故障与数据完整性失败不同"},
	{"Evidence Gate 通过后是否继续检索？", "Evidence Gate 未通过后是否继续检索？", "门禁结果相反"},
	{"Early Exit 是否减少 Tool Call？", "普通 Evidence Gate 是否一定减少 Tool Call？", "实验优化与基础门禁不同"},
	{"OTel 导出失败是否影响回答？", "PostgreSQL 写入失败是否影响回答？", "可降级观测与关键事实库不同"},
	{"缓存 TTL 是 24 小时吗？", "缓存 TTL 是 48 小时吗？", "时间长度不同"},
	{"缓存最多保存 1000 条吗？", "缓存最多保存 10000 条吗？", "容量约束不同"},
	{"引用最多允许 8 条吗？", "检索候选最多允许 8 条吗？", "引用上限与候选上限不同"},
	{"语义阈值 0.94 能上线吗？", "语义阈值未达到 98% Precision 能上线吗？", "数值阈值与验收门禁不同"},
}

var temporalDraftPairs = []draftPair{
	{"知识文档的发布流程是什么？", "今天知识文档的最新发布流程是什么？", "候选依赖今天和最新状态"},
	{"设备点检制度包含什么？", "截至今天设备点检制度包含什么？", "候选依赖当前日期"},
	{"系统支持哪些模型 Provider？", "目前系统最新支持哪些模型 Provider？", "候选依赖当前配置"},
	{"答案缓存配置是什么？", "现在答案缓存配置是什么？", "候选依赖运行时配置"},
	{"哪个知识版本是 current？", "最近发布的知识版本是哪一个？", "候选依赖最新发布状态"},
	{"OCR Provider 如何配置？", "本周 OCR Provider 是否发生变更？", "候选依赖近期变更"},
	{"VLM 的模型 ID 是什么？", "当前线上 VLM 的模型 ID 是什么？", "候选依赖部署状态"},
	{"知识库有哪些制度？", "2026-08-14 知识库有哪些制度？", "候选绑定具体日期"},
	{"诊断 Worker 如何部署？", "今天有多少诊断 Worker 在线？", "架构知识与实时状态不同"},
	{"SSE 如何续传？", "刚才那次 SSE 断线为什么没有续传？", "候选依赖刚才事件"},
	{"缓存命中率如何定义？", "最近一小时缓存命中率是多少？", "指标定义与实时指标不同"},
	{"Embedding Profile 是什么？", "当前 active Embedding Profile 是哪个？", "概念与运行状态不同"},
	{"知识更新如何失效缓存？", "上一次知识更新失效了多少缓存？", "机制与历史事件不同"},
	{"Outbox Relay 如何工作？", "目前 Outbox 积压多少条？", "机制与实时队列状态不同"},
	{"PostgreSQL 连接池如何配置？", "现在 PostgreSQL 有多少活跃连接？", "配置知识与实时状态不同"},
	{"Tool 调用失败如何降级？", "今天哪个 Tool 失败最多？", "策略与实时统计不同"},
	{"知识解析吞吐量如何测？", "最新一次知识解析吞吐量是多少？", "测量方法与最近结果不同"},
	{"RAG Recall@K 如何计算？", "当前版本 RAG Recall@K 是多少？", "指标定义与当前结果不同"},
	{"Prompt 版本如何配置？", "昨天上线的 Prompt 版本是什么？", "配置机制与历史部署不同"},
	{"模型 Token 如何统计？", "本月已经消耗多少模型 Token？", "统计方式与当前账单不同"},
}

var contextDraftPairs = []draftPair{
	{"知识文档如何发布？", "继续上面的发布操作。", "候选依赖上文"},
	{"设备点检周期是什么？", "这个周期为什么这样设置？", "候选使用指代词"},
	{"SQL 查询为什么失败？", "刚才那条 SQL 为什么失败？", "候选依赖历史 Tool 结果"},
	{"诊断任务如何创建？", "给这个工单创建诊断任务。", "候选依赖已选工单"},
	{"诊断报告如何解读？", "解释一下右侧这份报告。", "候选依赖已选报告"},
	{"附件如何参与诊断？", "结合我上传的附件重新判断。", "候选依赖附件"},
	{"知识问答如何引用来源？", "根据我个人知识库里的文档回答。", "候选依赖 Personal 知识"},
	{"Web Search Tool 有什么作用？", "去网上查一下这个报错。", "候选依赖时效性 Web Search"},
	{"GitHub 代码如何检索？", "在刚才授权的仓库里继续搜索。", "候选依赖历史授权范围"},
	{"会话摘要如何工作？", "把前面的讨论总结一下。", "候选依赖完整会话"},
	{"模型如何选择 Tool？", "用你刚才没用过的那个工具再试一次。", "候选依赖上轮工具轨迹"},
	{"知识版本如何发布？", "按第二种方案发布它。", "候选依赖前文方案和对象"},
	{"OCR 结果如何校验？", "检查这张图片识别得对不对。", "候选依赖图片"},
	{"表格如何向量化？", "把这个 Excel 表格加入知识库。", "候选依赖上传文件"},
	{"任务如何取消？", "取消我正在看的任务。", "候选依赖当前任务"},
	{"工单如何读取？", "看看工单 MES-1024 的详细信息。", "候选要求实时业务读取"},
	{"数据库证据如何获取？", "查一下设备 EQ-7788 的当前状态。", "候选要求实时 ERP 查询"},
	{"知识缓存如何命中？", "为什么我上一轮没有命中缓存？", "候选依赖上一轮运行记录"},
	{"Agent 如何返回引用？", "把刚才答案的第三条引用展开。", "候选依赖历史引用"},
	{"诊断结论如何生成？", "证据还不够，我补充这个文件后再诊断。", "候选依赖历史任务和新附件"},
}
