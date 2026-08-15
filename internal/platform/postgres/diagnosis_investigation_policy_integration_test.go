//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/diagnosis"
	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type policyIntegrationFixture struct {
	db             *gorm.DB
	tx             *gorm.DB
	ctx            context.Context
	ownerID        uuid.UUID
	dataSourceID   uuid.UUID
	externalCaseID uuid.UUID
}

func newPolicyIntegrationFixture(t *testing.T) *policyIntegrationFixture {
	t.Helper()
	dsn := os.Getenv("MESGUARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MESGUARD_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get test postgres sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin fixture transaction: %v", tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })

	fixture := &policyIntegrationFixture{
		db: db, tx: tx, ctx: ctx,
		ownerID: uuid.New(), dataSourceID: uuid.New(), externalCaseID: uuid.New(),
	}
	if err := tx.Exec(`
INSERT INTO users (id, username, display_name, password_hash, role, status, must_change_password)
VALUES (?, ?, 'Policy Owner', 'integration-hash', 'analyst', 'active', false)`,
		fixture.ownerID, "policy_owner_"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO data_sources (id, code, name, source_type, source_role, environment, safety_mode, status)
VALUES (?, ?, 'Policy Source', 'sqlserver', 'case_source', 'integration', 'read_only', 'active')`,
		fixture.dataSourceID, "policy-source-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if err := tx.Exec(`
INSERT INTO external_cases (id, data_source_id, external_case_key, external_case_type, last_seen_at)
VALUES (?, ?, ?, 'incident', now())`,
		fixture.externalCaseID, fixture.dataSourceID, "POLICY-"+uuid.NewString()[:8]).Error; err != nil {
		t.Fatalf("insert external case: %v", err)
	}
	return fixture
}

func (f *policyIntegrationFixture) caseItem() *externalcase.ExternalCase {
	return &externalcase.ExternalCase{
		ID: f.externalCaseID, DataSourceID: f.dataSourceID, ExternalCaseKey: "POLICY-1001",
		CaseType: "incident", Title: "Policy fixture", SourceFingerprint: "sha256:policy-source",
		ReportedAt: time.Now().UTC(), SourceUpdatedAt: time.Now().UTC(),
	}
}

func (f *policyIntegrationFixture) newService(t *testing.T, builder diagnosis.InvestigationPolicyBuilder) *diagnosis.DiagnosisTaskService {
	t.Helper()
	service, err := diagnosis.NewDiagnosisTaskService(
		NewDiagnosisTaskRepository(f.tx), integrationCaseReader{item: f.caseItem()}, builder,
	)
	if err != nil {
		t.Fatalf("NewDiagnosisTaskService(): %v", err)
	}
	return service
}

func TestDiagnosisTaskRepositoryPersistsAndReplaysFirstFrozenPolicy(t *testing.T) {
	fixture := newPolicyIntegrationFixture(t)
	caseID := fixture.externalCaseID
	sourceID := fixture.dataSourceID
	firstBuilder := mustIntegrationPolicyBuilder(t, sourceID)
	// 部署配置变化后：同一幂等命令由"只有 case 上限"的新 Builder 重建服务。
	secondBuilder := mustIntegrationPolicyBuilder(t)

	firstService := fixture.newService(t, firstBuilder)
	input := diagnosis.CreateTaskInput{
		ExternalCaseID: caseID, ExpectedSourceFingerprint: "sha256:policy-source",
		RequestText: "检查任务状态", RequestScope: map[string]any{"source": "integration"},
		IdempotencyKey: "policy-replay-key", CorrelationID: uuid.New(),
	}
	created, err := firstService.Create(fixture.ctx, diagnosis.TaskActor{UserID: fixture.ownerID}, input)
	if err != nil {
		t.Fatalf("first Create(): %v", err)
	}
	if created.Replayed {
		t.Fatal("first Create() replayed unexpectedly")
	}
	var stored struct {
		Policy  []byte `gorm:"column:investigation_policy"`
		Version int    `gorm:"column:investigation_policy_schema_version"`
		Mode    string `gorm:"column:investigation_policy_mode"`
	}
	if err := fixture.tx.Raw(`
SELECT investigation_policy, investigation_policy_schema_version, investigation_policy_mode
FROM diagnosis_tasks WHERE id = ?`, created.Task.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read stored policy: %v", err)
	}
	if stored.Version != diagnosis.InvestigationPolicySchemaVersion {
		t.Fatalf("stored schema version = %d", stored.Version)
	}
	if stored.Mode != string(diagnosis.InvestigationPolicyModeFrozen) {
		t.Fatalf("stored policy mode = %q, want %q", stored.Mode, diagnosis.InvestigationPolicyModeFrozen)
	}
	firstPolicy, err := agentruntime.UnmarshalInvestigationPolicy(stored.Policy)
	if err != nil {
		t.Fatalf("decode stored policy: %v", err)
	}
	if !firstPolicy.Permissions().Has(agentruntime.PermissionCaseRead) ||
		!firstPolicy.Permissions().Has(agentruntime.PermissionKnowledgeRead) ||
		!firstPolicy.Grants().AllowsExternalCase(caseID) ||
		!firstPolicy.Grants().AllowsDataSource(sourceID) {
		t.Fatalf("stored policy = %v", firstPolicy)
	}

	// 幂等回放：部署配置变化不能制造冲突，必须回放首次冻结的 Policy。
	secondService := fixture.newService(t, secondBuilder)
	replayed, err := secondService.Create(fixture.ctx, diagnosis.TaskActor{UserID: fixture.ownerID}, input)
	if err != nil {
		t.Fatalf("replay Create(): %v", err)
	}
	if !replayed.Replayed || replayed.Task.ID != created.Task.ID {
		t.Fatalf("replay result = %+v", replayed)
	}
	if err := fixture.tx.Raw(`
SELECT investigation_policy FROM diagnosis_tasks WHERE id = ?`, created.Task.ID).Scan(&stored.Policy).Error; err != nil {
		t.Fatalf("re-read stored policy: %v", err)
	}
	replayedPolicy, err := agentruntime.UnmarshalInvestigationPolicy(stored.Policy)
	if err != nil {
		t.Fatalf("decode replayed policy: %v", err)
	}
	if !replayedPolicy.Grants().AllowsDataSource(sourceID) {
		t.Fatalf("replay replaced the first frozen policy: %v", replayedPolicy)
	}
	if err := fixture.tx.Raw(`
SELECT investigation_policy_mode FROM diagnosis_tasks WHERE id = ?`, created.Task.ID).Scan(&stored.Mode).Error; err != nil {
		t.Fatalf("re-read stored mode: %v", err)
	}
	if stored.Mode != string(diagnosis.InvestigationPolicyModeFrozen) {
		t.Fatalf("replay mutated policy mode to %q", stored.Mode)
	}
}

func TestDiagnosisWorkerRepositoryLoadsFrozenLegacyAndRejectsCorruptPolicy(t *testing.T) {
	fixture := newPolicyIntegrationFixture(t)
	taskService := fixture.newService(t, mustIntegrationPolicyBuilder(t, fixture.dataSourceID))
	created, err := taskService.Create(fixture.ctx, diagnosis.TaskActor{UserID: fixture.ownerID}, diagnosis.CreateTaskInput{
		ExternalCaseID: fixture.externalCaseID, ExpectedSourceFingerprint: "sha256:policy-source",
		RequestText: "检查任务状态", IdempotencyKey: uuid.NewString(), CorrelationID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	leaseService, err := diagnosis.NewTaskExecutionService(NewDiagnosisTaskRepository(fixture.tx), time.Minute)
	if err != nil {
		t.Fatalf("NewTaskExecutionService(): %v", err)
	}
	claim, err := leaseService.Claim(fixture.ctx, created.Task.ID, "policy-integration-worker")
	if err != nil || claim.Lease == nil {
		t.Fatalf("Claim(): result=%+v err=%v", claim, err)
	}
	workerRepository := NewDiagnosisWorkerRepository(fixture.tx)

	// 1. 新任务：LoadTask 解码出与冻结完全一致的 Policy。
	task, err := workerRepository.LoadTask(fixture.ctx, *claim.Lease, time.Now().UTC())
	if err != nil {
		t.Fatalf("LoadTask(): %v", err)
	}
	if task.Policy == nil {
		t.Fatal("new task lost its frozen policy")
	}
	if task.Policy.SchemaVersion() != diagnosis.InvestigationPolicySchemaVersion ||
		!task.Policy.Permissions().Has(agentruntime.PermissionCaseRead) ||
		!task.Policy.Grants().AllowsExternalCase(fixture.externalCaseID) ||
		!task.Policy.Grants().AllowsDataSource(fixture.dataSourceID) {
		t.Fatalf("loaded policy = %v", task.Policy)
	}

	// 2. migration 前旧任务：mode='legacy' + 双 NULL 是唯一合法兼容状态，
	//    Policy=nil；双 NULL 本身不再单独代表 legacy。
	if err := fixture.tx.Exec(`
UPDATE diagnosis_tasks SET investigation_policy_mode = 'legacy',
    investigation_policy = NULL, investigation_policy_schema_version = NULL
WHERE id = ?`, created.Task.ID).Error; err != nil {
		t.Fatalf("null policy columns: %v", err)
	}
	task, err = workerRepository.LoadTask(fixture.ctx, *claim.Lease, time.Now().UTC())
	if err != nil {
		t.Fatalf("legacy LoadTask(): %v", err)
	}
	if task.Policy != nil {
		t.Fatalf("legacy task policy = %v, want nil", task.Policy)
	}

	// 3. 未知字段的 JSONB（语义损坏）：strict codec 拒绝，fail-closed。
	if err := fixture.tx.Exec(`
UPDATE diagnosis_tasks SET investigation_policy_mode = 'frozen',
    investigation_policy = '{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}'::jsonb,
    investigation_policy_schema_version = 1
WHERE id = ?`, created.Task.ID).Error; err != nil {
		t.Fatalf("corrupt policy columns: %v", err)
	}
	if _, err := workerRepository.LoadTask(fixture.ctx, *claim.Lease, time.Now().UTC()); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("corrupt policy LoadTask() error = %v, want ErrInvalidTask", err)
	}

	// 4. 列版本与 payload 版本不一致：同样 fail-closed。
	if err := fixture.tx.Exec(`
UPDATE diagnosis_tasks SET investigation_policy_mode = 'frozen',
    investigation_policy = '{"schemaVersion":1,"permissions":["case.read"],"grants":{}}'::jsonb,
    investigation_policy_schema_version = 2
WHERE id = ?`, created.Task.ID).Error; err != nil {
		t.Fatalf("mismatched policy columns: %v", err)
	}
	if _, err := workerRepository.LoadTask(fixture.ctx, *claim.Lease, time.Now().UTC()); !errors.Is(err, diagnosis.ErrInvalidTask) {
		t.Fatalf("mismatched policy LoadTask() error = %v, want ErrInvalidTask", err)
	}

	// 5. mode 与两列组合不一致时数据库拒绝：每条违例都用 SAVEPOINT 隔离，
	//    避免前一条 CHECK 失败把事务置为 aborted 后掩盖后续断言。
	violations := []struct {
		name       string
		constraint string
		sql        string
	}{
		{
			name:       "legacy with policy columns",
			constraint: "diagnosis_tasks_investigation_policy_mode_legacy_pair_check",
			sql: `UPDATE diagnosis_tasks SET investigation_policy_mode = 'legacy',
    investigation_policy = '{"schemaVersion":1,"permissions":["case.read"],"grants":{}}'::jsonb,
    investigation_policy_schema_version = 1 WHERE id = ?`,
		},
		{
			name:       "frozen with NULL policy columns",
			constraint: "diagnosis_tasks_investigation_policy_mode_frozen_pair_check",
			sql: `UPDATE diagnosis_tasks SET investigation_policy_mode = 'frozen',
    investigation_policy = NULL, investigation_policy_schema_version = NULL WHERE id = ?`,
		},
		{
			name:       "illegal mode value",
			constraint: "diagnosis_tasks_investigation_policy_mode_check",
			sql:        `UPDATE diagnosis_tasks SET investigation_policy_mode = 'Frozen' WHERE id = ?`,
		},
		{
			name:       "legacy with one-sided payload",
			constraint: "diagnosis_tasks_investigation_policy_pair_check",
			sql: `UPDATE diagnosis_tasks SET investigation_policy_mode = 'legacy',
    investigation_policy = '{}'::jsonb, investigation_policy_schema_version = NULL WHERE id = ?`,
		},
	}
	for _, violation := range violations {
		if err := fixture.tx.Exec("SAVEPOINT policy_violation").Error; err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		result := fixture.tx.Exec(violation.sql, created.Task.ID)
		if result.Error == nil {
			t.Fatalf("violation %q was accepted by the database", violation.name)
		}
		if !strings.Contains(result.Error.Error(), violation.constraint) {
			t.Fatalf("violation %q error = %v, want constraint %q",
				violation.name, result.Error, violation.constraint)
		}
		if err := fixture.tx.Exec("ROLLBACK TO SAVEPOINT policy_violation").Error; err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
}

// TestDiagnosisTaskRepositoryRejectsPolicyModeContractViolationsAgainstPostgres
// 验证 Repository 创建路径的 mode 合同：frozen + 有效 Policy 可写入；mode 非
// frozen、payload 缺失、版本非法/不一致或 codec 损坏都在 INSERT 前 fail-closed，
// 不产生任何任务行，也绝不把缺失 Policy 的新任务自动转换成 legacy。
func TestDiagnosisTaskRepositoryRejectsPolicyModeContractViolationsAgainstPostgres(t *testing.T) {
	fixture := newPolicyIntegrationFixture(t)
	repository := NewDiagnosisTaskRepository(fixture.tx)
	validPayload, validVersion := mustValidFrozenPolicyBytes(t)

	baseRecord := func() diagnosis.CreateTaskRecord {
		return diagnosis.CreateTaskRecord{
			CreatedBy:                        fixture.ownerID,
			ExternalCaseID:                   fixture.externalCaseID,
			IdempotencyKey:                   "policy-validation-key",
			RequestFingerprint:               "sha256:policy-validation",
			RequestText:                      "检查任务状态",
			RequestScope:                     json.RawMessage(`{}`),
			RequestScopeSchemaVersion:        1,
			InvestigationPolicy:              append(json.RawMessage(nil), validPayload...),
			InvestigationPolicySchemaVersion: validVersion,
			InvestigationPolicyMode:          diagnosis.InvestigationPolicyModeFrozen,
			Snapshot: diagnosis.CaseSnapshotRecord{
				Payload: json.RawMessage(`{}`), PayloadSchemaVersion: 1, ContentHash: "sha256:snap",
				SourceReadAt: time.Now().UTC(), RedactionStatus: "redacted", TruncationStatus: "complete",
			},
			CorrelationID: uuid.New(),
			CreatedAt:     time.Now().UTC(),
		}
	}

	countRows := func(table string) int64 {
		var count int64
		if err := fixture.tx.Raw("SELECT COUNT(*) FROM " + table).Scan(&count).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return count
	}

	// 1. frozen + 有效 Policy：完整写入成功。
	created, err := repository.CreateTask(fixture.ctx, baseRecord())
	if err != nil {
		t.Fatalf("valid frozen CreateTask: %v", err)
	}
	if created.Replayed || created.Task.ID == uuid.Nil {
		t.Fatalf("valid frozen result = %+v", created)
	}
	if countRows("diagnosis_tasks") != 1 || countRows("case_snapshots") != 1 {
		t.Fatalf("valid frozen create did not insert exactly one task/snapshot row")
	}

	// 2. 合同违例：全部在 INSERT 前拒绝，且行数不增长。
	cases := []struct {
		name   string
		mutate func(*diagnosis.CreateTaskRecord)
	}{
		{
			name: "legacy mode",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicyMode = diagnosis.InvestigationPolicyModeLegacy
			},
		},
		{
			name: "empty mode",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicyMode = ""
			},
		},
		{
			name: "nil policy payload",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicy = nil
			},
		},
		{
			name: "zero schema version",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicySchemaVersion = 0
			},
		},
		{
			name: "column version disagrees with payload",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicySchemaVersion = validVersion + 1
			},
		},
		{
			name: "corrupt payload",
			mutate: func(record *diagnosis.CreateTaskRecord) {
				record.InvestigationPolicy = json.RawMessage(
					`{"schemaVersion":1,"permissions":["case.read"],"grants":{},"unknownField":true}`)
			},
		},
	}
	for _, testCase := range cases {
		record := baseRecord()
		testCase.mutate(&record)
		if _, err := repository.CreateTask(fixture.ctx, record); !errors.Is(err, diagnosis.ErrInvalidTask) {
			t.Fatalf("%s error = %v, want ErrInvalidTask", testCase.name, err)
		}
		if countRows("diagnosis_tasks") != 1 || countRows("case_snapshots") != 1 {
			t.Fatalf("%s violated fail-closed: a row was inserted", testCase.name)
		}
	}
}
