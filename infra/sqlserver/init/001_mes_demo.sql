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

IF OBJECT_ID(N'dbo.Tickets', N'U') IS NULL
BEGIN
    CREATE TABLE dbo.Tickets (
        TicketID NVARCHAR(32) NOT NULL PRIMARY KEY,
        ProductVersion NVARCHAR(32) NOT NULL,
        Module NVARCHAR(64) NOT NULL,
        Symptom NVARCHAR(1000) NOT NULL,
        Environment NVARCHAR(256) NOT NULL,
        Status NVARCHAR(32) NOT NULL,
        CreatedAt DATETIME2 NOT NULL
    );
END;
GO

IF NOT EXISTS (SELECT 1 FROM dbo.Tickets WHERE TicketID = N'TKT-1001')
BEGIN
    INSERT INTO dbo.Tickets
        (TicketID, ProductVersion, Module, Symptom, Environment, Status, CreatedAt)
    VALUES
        (N'TKT-1001', N'MES 3.2', N'Production Reporting',
         N'Month-end production report takes longer than one minute to load.',
         N'SQL Server 2022 / demo factory A', N'Open', SYSUTCDATETIME());
END;
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

IF NOT EXISTS (SELECT 1 FROM dbo.ProductionOrders WHERE OrderNo = N'PO-202607-001')
BEGIN
    INSERT INTO dbo.ProductionOrders
        (OrderID, OrderNo, ProductCode, Status, StartedAt, CompletedAt)
    VALUES
        (1001, N'PO-202607-001', N'P-100', N'Released', NULL, NULL);
END;
GO
