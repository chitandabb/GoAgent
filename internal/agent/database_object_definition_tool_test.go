package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubDatabaseObjectDefinitionReader struct {
	definition string
	objectType string
	truncated  bool
	err        error
	schema     string
	objectName string
}

func (s *stubDatabaseObjectDefinitionReader) GetObjectDefinition(
	_ context.Context, schemaName, objectName string,
) (string, string, bool, error) {
	s.schema, s.objectName = schemaName, objectName
	return s.definition, s.objectType, s.truncated, s.err
}

func TestDatabaseObjectDefinitionToolReturnsStructuredDefinition(t *testing.T) {
	reader := &stubDatabaseObjectDefinitionReader{
		definition: "CREATE PROCEDURE dbo.usp_Ticket AS SELECT 1",
		objectType: "SQL_STORED_PROCEDURE",
		truncated:  true,
	}
	current, err := NewDatabaseObjectDefinitionTool(reader)
	if err != nil {
		t.Fatalf("NewDatabaseObjectDefinitionTool: %v", err)
	}
	result, err := current.InvokableRun(context.Background(), `{"schema":"dbo","objectName":"usp_Ticket"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.Contains(result, `"objectType":"SQL_STORED_PROCEDURE"`) ||
		!strings.Contains(result, `"truncated":true`) {
		t.Fatalf("unexpected result: %s", result)
	}
	if reader.schema != "dbo" || reader.objectName != "usp_Ticket" {
		t.Fatalf("reader received %q.%q", reader.schema, reader.objectName)
	}
}

func TestDatabaseObjectDefinitionToolRejectsUnsafeIdentifiers(t *testing.T) {
	reader := &stubDatabaseObjectDefinitionReader{}
	current, err := NewDatabaseObjectDefinitionTool(reader)
	if err != nil {
		t.Fatalf("NewDatabaseObjectDefinitionTool: %v", err)
	}
	for _, input := range []string{
		`{"schema":"dbo;DROP TABLE x","objectName":"usp_Ticket"}`,
		`{"schema":"dbo","objectName":"usp_Ticket --"}`,
		`{"schema":" dbo","objectName":"usp_Ticket"}`,
	} {
		if _, err = current.InvokableRun(context.Background(), input); err == nil {
			t.Fatalf("InvokableRun(%s) accepted unsafe identifier", input)
		}
	}
	if reader.schema != "" || reader.objectName != "" {
		t.Fatal("unsafe input reached the database reader")
	}
}

func TestDatabaseObjectDefinitionToolPropagatesReaderError(t *testing.T) {
	want := errors.New("dial tcp sql.internal.example:1433: database unavailable")
	current, err := NewDatabaseObjectDefinitionTool(&stubDatabaseObjectDefinitionReader{err: want})
	if err != nil {
		t.Fatalf("NewDatabaseObjectDefinitionTool: %v", err)
	}
	if _, err = current.InvokableRun(context.Background(), `{"schema":"dbo","objectName":"usp_Ticket"}`); !errors.Is(err, ErrDatabaseObjectDefinitionUnavailable) {
		t.Fatalf("InvokableRun error = %v, want safe unavailable error", err)
	} else if strings.Contains(err.Error(), "sql.internal.example") {
		t.Fatalf("InvokableRun leaked connection details: %v", err)
	}
}
