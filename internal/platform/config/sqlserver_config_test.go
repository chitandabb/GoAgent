package config

import "testing"

func TestSQLServerConfigRejectsArbitrarySQLRelation(t *testing.T) {
	cfg := validSQLServerConfig()
	cfg.CaseMapping.Relation = "dbo.Cases; DROP TABLE users"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted arbitrary SQL relation")
	}
}

func TestSQLServerConfigRejectsUnknownCanonicalField(t *testing.T) {
	cfg := validSQLServerConfig()
	cfg.CaseMapping.Fields["password"] = "PasswordHash"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted unsupported canonical field")
	}
}

func TestSQLServerConfigRejectsUnsafeAttributeMapping(t *testing.T) {
	cfg := validSQLServerConfig()
	cfg.CaseMapping.Attributes["bad-key"] = "SafeColumn"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted unsafe attribute name")
	}
}

func TestSQLServerConfigAcceptsValidatedMapping(t *testing.T) {
	if err := validSQLServerConfig().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func validSQLServerConfig() SQLServerConfig {
	return SQLServerConfig{
		Enabled: true, ID: "8d5c67dc-4c09-4ee5-9e80-4d822303dc35", Code: "erp",
		Name: "ERP", Environment: "test", Host: "localhost", Port: 1433,
		User: "reader", Database: "SUPPORT", PasswordEnv: "SQL_PASSWORD",
		Encrypt: "disable",
		MaxOpen: 5, MaxIdle: 1, QueryTimeoutMillis: 1000, MaxTextBytes: 512, MaxResultBytes: 1024,
		CaseMapping: SQLServerCaseMapping{
			Relation: "dbo.Cases",
			Fields: map[string]string{
				"externalCaseKey": "CaseID", "title": "Title", "description": "Description",
				"status": "Status", "reportedAt": "ReportedAt", "sourceUpdatedAt": "UpdatedAt",
			},
			Attributes:     map[string]string{"reporterDepartment": "ReporterDepartment"},
			StatusValues:   map[string]string{"New": "open"},
			PriorityValues: map[string]string{"Urgent": "high"},
		},
		AttachmentMapping: SQLServerObjectMapping{
			Relation: "dbo.Attachments",
			Fields: map[string]string{
				"externalCaseKey": "CaseID", "externalAttachmentKey": "AttachmentID",
				"fileName": "FileName", "mediaType": "MediaType", "sizeBytes": "SizeBytes",
				"objectKey": "ObjectKey", "contentHash": "ContentHash", "sourceUpdatedAt": "UpdatedAt",
			},
		},
	}
}
