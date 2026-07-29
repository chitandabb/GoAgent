SET NOCOUNT ON;
GO

IF DB_ID(N'SUPPORT_DEMO') IS NULL
BEGIN
    CREATE DATABASE SUPPORT_DEMO;
END;
GO

IF DB_ID(N'MES_DEMO') IS NULL
BEGIN
    CREATE DATABASE MES_DEMO;
END;
GO

USE SUPPORT_DEMO;
GO

-- 演示库每次播种都恢复到确定状态，便于重复联调和集成测试。
DROP VIEW IF EXISTS dbo.v_MESGuardExternalCaseAttachments;
DROP VIEW IF EXISTS dbo.v_MESGuardExternalCases;
DROP TABLE IF EXISTS dbo.TicketAttachments;
DROP TABLE IF EXISTS dbo.TicketProductionContexts;
DROP TABLE IF EXISTS dbo.Tickets;
GO

CREATE TABLE dbo.Tickets (
    TicketID NVARCHAR(32) NOT NULL PRIMARY KEY,
    CaseType NVARCHAR(32) NOT NULL,
    Title NVARCHAR(200) NOT NULL,
    Description NVARCHAR(4000) NOT NULL,
    Category NVARCHAR(64) NULL,
    Module NVARCHAR(64) NULL,
    Status NVARCHAR(32) NOT NULL,
    Priority NVARCHAR(32) NOT NULL,
    OccurredAt DATETIME2 NULL,
    ReportedAt DATETIME2 NOT NULL,
    SourceUpdatedAt DATETIME2 NOT NULL,
    CustomerCode NVARCHAR(64) NULL,
    CustomerName NVARCHAR(128) NULL,
    ProductCode NVARCHAR(64) NULL,
    ProductName NVARCHAR(128) NULL,
    ProductVersion NVARCHAR(64) NULL,
    SourceSystem NVARCHAR(64) NOT NULL,
    DeploymentEnvironment NVARCHAR(64) NULL,
    BusinessDatabaseAlias NVARCHAR(128) NULL,
    ReporterDepartment NVARCHAR(128) NULL,
    ImpactScope NVARCHAR(256) NULL
);

CREATE TABLE dbo.TicketProductionContexts (
    TicketID NVARCHAR(32) NOT NULL PRIMARY KEY
        REFERENCES dbo.Tickets(TicketID) ON DELETE CASCADE,
    WorkOrderNo NVARCHAR(64) NULL,
    WorkpieceNo NVARCHAR(64) NULL,
    MaterialCode NVARCHAR(64) NULL,
    BatchNo NVARCHAR(64) NULL,
    SerialNo NVARCHAR(128) NULL,
    FactoryCode NVARCHAR(64) NULL,
    WorkshopCode NVARCHAR(64) NULL,
    ProductionLineCode NVARCHAR(64) NULL,
    WorkstationCode NVARCHAR(64) NULL,
    EquipmentCode NVARCHAR(64) NULL
);

CREATE TABLE dbo.TicketAttachments (
    AttachmentID NVARCHAR(64) NOT NULL PRIMARY KEY,
    TicketID NVARCHAR(32) NOT NULL
        REFERENCES dbo.Tickets(TicketID) ON DELETE CASCADE,
    FileName NVARCHAR(255) NOT NULL,
    MediaType NVARCHAR(128) NOT NULL,
    SizeBytes BIGINT NOT NULL,
    ObjectKey NVARCHAR(512) NOT NULL,
    ContentHash NVARCHAR(128) NOT NULL,
    SourceUpdatedAt DATETIME2 NOT NULL,
    CONSTRAINT CK_TicketAttachments_Size CHECK (SizeBytes >= 0)
);

INSERT INTO dbo.Tickets
    (TicketID, CaseType, Title, Description, Category, Module, Status, Priority,
     OccurredAt, ReportedAt, SourceUpdatedAt, CustomerCode, CustomerName,
     ProductCode, ProductName, ProductVersion, SourceSystem,
     DeploymentEnvironment, BusinessDatabaseAlias, ReporterDepartment, ImpactScope)
VALUES
    (N'TKT-1001', N'production_fault', N'报工后库存未更新',
     N'末道工序 OP50 报工成功，但成品库存未生成入库记录，同产线其他工单正常。',
     N'库存联动', N'Production Reporting', N'New', N'Urgent',
     '2026-07-25T08:10:00', '2026-07-25T08:30:00', '2026-07-29T01:00:00',
     N'CUST-A', N'苏州精密制造', N'MES-PRO', N'MES-Pro', N'v5.2.1',
     N'CompanyERP', N'customer-production', N'customer-a-prod', N'客户成功部', N'单工单末道工序'),
    (N'TKT-1002', N'production_fault', N'工单无法关闭，提示存在未完成工序',
     N'现场确认 OP20 已完成并有纸质记录，但系统关闭工单时仍判定该工序未完成。',
     N'工序流转', N'Work Order', N'New', N'Normal',
     '2026-07-26T03:20:00', '2026-07-26T04:00:00', '2026-07-28T09:30:00',
     N'CUST-B', N'无锡智联装备', N'MES-PRO', N'MES-Pro', N'v5.1.8',
     N'CompanyERP', N'customer-production', N'customer-b-prod', N'实施服务部', N'单工单关闭流程'),
    (N'TKT-1003', N'data_fault', N'条码扫描重复报工',
     N'同一序列号扫描一次后产生两条报工记录，界面卡顿后连续提示两次成功。',
     N'数据采集', N'Barcode Collection', N'Investigating', N'Urgent',
     '2026-07-27T01:05:00', '2026-07-27T01:20:00', '2026-07-29T02:10:00',
     N'CUST-A', N'苏州精密制造', N'MES-PRO', N'MES-Pro', N'v5.2.1',
     N'CompanyERP', N'customer-production', N'customer-a-prod', N'客户成功部', N'单设备单批次'),
    (N'TKT-1004', N'performance', N'排产计划界面加载超时',
     N'工作日上午八点打开排产计划超过一分钟，升级产品版本后开始出现。',
     N'性能', N'Planning', N'Resolved', N'Low',
     '2026-07-24T00:00:00', '2026-07-24T00:30:00', '2026-07-28T12:00:00',
     N'CUST-C', N'南通宏远机械', N'MES-LITE', N'MES-Lite', N'v3.4.0',
     N'CompanyERP', N'customer-production', N'customer-c-prod', N'产品支持部', N'早班排产用户');

INSERT INTO dbo.TicketProductionContexts
    (TicketID, WorkOrderNo, WorkpieceNo, MaterialCode, BatchNo, SerialNo,
     FactoryCode, WorkshopCode, ProductionLineCode, WorkstationCode, EquipmentCode)
VALUES
    (N'TKT-1001', N'WO-202607-00128', N'WP-88310', N'MAT-FG-100', N'BATCH-0725-A', NULL,
     N'FAC-A', N'WS-MACHINING', N'LINE-03', N'ST-OP50', N'CNC-032'),
    (N'TKT-1002', N'WO-202607-00131', N'WP-77320', N'MAT-PUMP-220', N'BATCH-0724-B', NULL,
     N'FAC-B', N'WS-ASSEMBLY', N'LINE-01', N'ST-OP20', N'ASM-011'),
    (N'TKT-1003', N'WO-202607-00119', N'WP-99008', N'MAT-SHAFT-090', N'BATCH-0727-C', N'SN-20260727-000881',
     N'FAC-A', N'WS-MACHINING', N'LINE-03', N'ST-SCAN-02', N'SCANNER-204'),
    (N'TKT-1004', NULL, NULL, NULL, NULL, NULL,
     N'FAC-C', N'WS-PLANNING', NULL, NULL, NULL);

INSERT INTO dbo.TicketAttachments
    (AttachmentID, TicketID, FileName, MediaType, SizeBytes, ObjectKey, ContentHash, SourceUpdatedAt)
VALUES
    (N'ERP-ATT-1001-1', N'TKT-1001', N'库存查询截图.png', N'image/png', 182340,
     N'erp/TKT-1001/inventory-screen.png', N'sha256:581665b87d23914b386b2f24d8908c4fd87b6af165986d45de3181477292c517', '2026-07-25T08:25:00'),
    (N'ERP-ATT-1002-1', N'TKT-1002', N'现场工序流转卡.jpg', N'image/jpeg', 245901,
     N'erp/TKT-1002/route-card.jpg', N'sha256:03a2b7e491527ede9d45f16a88800d387006180f4a8359b479d67a257ff01e5b', '2026-07-26T03:50:00'),
    (N'ERP-ATT-1003-1', N'TKT-1003', N'操作员聊天截图.png', N'image/png', 141802,
     N'erp/TKT-1003/operator-chat.png', N'sha256:7df7a61b27e430e0d46c390e9c9d88895bf5aa95604f4a6c4a8b1d06325e2c37', '2026-07-27T01:18:00'),
    (N'ERP-ATT-1003-2', N'TKT-1003', N'采集网关日志.txt', N'text/plain', 38640,
     N'erp/TKT-1003/gateway.log', N'sha256:cfad8cb63b0e2c948f732e8c09eb4f47902dc2dd9b691a2aa66b3dcdd05ae629', '2026-07-27T01:19:00');
GO

CREATE VIEW dbo.v_MESGuardExternalCases
AS
SELECT
    t.TicketID, t.CaseType, t.Title, t.Description, t.Category, t.Module,
    t.Status, t.Priority, t.OccurredAt, t.ReportedAt, t.SourceUpdatedAt,
    t.CustomerCode, t.CustomerName, t.ProductCode, t.ProductName,
    t.ProductVersion, p.WorkOrderNo, p.WorkpieceNo, p.MaterialCode,
    p.BatchNo, p.SerialNo, p.FactoryCode, p.WorkshopCode,
    p.ProductionLineCode, p.WorkstationCode, p.EquipmentCode,
    t.SourceSystem, t.DeploymentEnvironment, t.BusinessDatabaseAlias,
    t.ReporterDepartment, t.ImpactScope
FROM dbo.Tickets AS t
LEFT JOIN dbo.TicketProductionContexts AS p ON p.TicketID = t.TicketID;
GO

CREATE VIEW dbo.v_MESGuardExternalCaseAttachments
AS
SELECT TicketID, AttachmentID, FileName, MediaType, SizeBytes, ObjectKey,
       ContentHash, SourceUpdatedAt
FROM dbo.TicketAttachments;
GO

USE master;
GO

IF SUSER_ID(N'mesguard_case_reader') IS NULL
BEGIN
    CREATE LOGIN mesguard_case_reader
        WITH PASSWORD = '$(ReaderPassword)', CHECK_POLICY = OFF, CHECK_EXPIRATION = OFF;
END
ELSE
BEGIN
    ALTER LOGIN mesguard_case_reader WITH PASSWORD = '$(ReaderPassword)';
END;
GO

USE SUPPORT_DEMO;
GO

IF USER_ID(N'mesguard_case_reader') IS NULL
BEGIN
    CREATE USER mesguard_case_reader FOR LOGIN mesguard_case_reader;
END;

GRANT CONNECT TO mesguard_case_reader;
GRANT SELECT ON OBJECT::dbo.v_MESGuardExternalCases TO mesguard_case_reader;
GRANT SELECT ON OBJECT::dbo.v_MESGuardExternalCaseAttachments TO mesguard_case_reader;
DENY INSERT, UPDATE, DELETE, EXECUTE TO mesguard_case_reader;
DENY CREATE TABLE, CREATE VIEW, CREATE PROCEDURE, CREATE FUNCTION TO mesguard_case_reader;
DENY ALTER ANY SCHEMA TO mesguard_case_reader;
GO

USE MES_DEMO;
GO

IF OBJECT_ID(N'dbo.ProductionOrders', N'U') IS NULL
BEGIN
    CREATE TABLE dbo.ProductionOrders (
        OrderID BIGINT NOT NULL PRIMARY KEY,
        OrderNo NVARCHAR(64) NOT NULL UNIQUE,
        ProductCode NVARCHAR(64) NOT NULL,
        Status NVARCHAR(32) NOT NULL,
        StartedAt DATETIME2 NULL,
        CompletedAt DATETIME2 NULL
    );
END;
GO

IF NOT EXISTS (SELECT 1 FROM dbo.ProductionOrders WHERE OrderNo = N'WO-202607-00128')
BEGIN
    INSERT INTO dbo.ProductionOrders
        (OrderID, OrderNo, ProductCode, Status, StartedAt, CompletedAt)
    VALUES
        (2001, N'WO-202607-00128', N'MAT-FG-100', N'Released', NULL, NULL);
END;
GO
