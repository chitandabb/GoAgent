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

func TestSQLServerConfigRejectsUnsafeInvestigationSchema(t *testing.T) {
	cfg := validSQLServerConfig()
	cfg.Investigation.AllowedSchemas = []string{"dbo; DROP TABLE Tickets"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted unsafe investigation schema")
	}
}

func TestSQLServerConfigRejectsAmbiguousInvestigationSchemas(t *testing.T) {
	for _, schemas := range [][]string{{" dbo"}, {"dbo", "dbo"}} {
		cfg := validSQLServerConfig()
		cfg.Investigation.AllowedSchemas = schemas
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted ambiguous investigation schemas: %q", schemas)
		}
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
		Investigation: SQLServerInvestigationConfig{
			AllowedSchemas: []string{"dbo"}, MaxQueryBytes: 8192, MaxRows: 100,
			MaxResultBytes: 262144, MaxConcurrentQueries: 2,
		},
	}
}
