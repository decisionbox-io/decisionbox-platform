package redshift

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshiftdata/types"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

func TestRedshiftProvider_Registered(t *testing.T) {
	meta, ok := gowarehouse.GetProviderMeta("redshift")
	if !ok {
		t.Fatal("redshift not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}
}

func TestRedshiftProvider_ConfigFields(t *testing.T) {
	meta, _ := gowarehouse.GetProviderMeta("redshift")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	for _, required := range []string{"workgroup", "cluster_id", "database", "dataset", "region"} {
		if !keys[required] {
			t.Errorf("missing config field: %s", required)
		}
	}
}

func TestRedshiftProvider_MissingIdentifier(t *testing.T) {
	p := &RedshiftProvider{
		database: "dev",
	}

	_, err := p.Query(nil, "SELECT 1", nil)
	if err == nil {
		t.Error("expected error when both workgroup and cluster_id are empty")
	}
}

func TestNormalizeRedshiftType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"integer", "INT64"},
		{"int", "INT64"},
		{"int4", "INT64"},
		{"bigint", "INT64"},
		{"int8", "INT64"},
		{"smallint", "INT64"},
		{"real", "FLOAT64"},
		{"double precision", "FLOAT64"},
		{"float8", "FLOAT64"},
		{"numeric(10,2)", "FLOAT64"},
		{"decimal(18,4)", "FLOAT64"},
		{"boolean", "BOOL"},
		{"bool", "BOOL"},
		{"date", "DATE"},
		{"timestamp", "TIMESTAMP"},
		{"timestamp without time zone", "TIMESTAMP"},
		{"timestamptz", "TIMESTAMP"},
		{"bytea", "BYTES"},
		{"varchar", "STRING"},
		{"character varying", "STRING"},
		{"text", "STRING"},
		{"char(10)", "STRING"},
		{"super", "STRING"},
	}
	for _, tt := range tests {
		got := normalizeRedshiftType(tt.input)
		if got != tt.want {
			t.Errorf("normalizeRedshiftType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedshiftProvider_GetDataset_Default(t *testing.T) {
	p := &RedshiftProvider{}
	if p.GetDataset() != "public" {
		t.Errorf("default dataset = %q, want public", p.GetDataset())
	}
}

func TestRedshiftProvider_GetDataset_Custom(t *testing.T) {
	p := &RedshiftProvider{dataset: "analytics"}
	if p.GetDataset() != "analytics" {
		t.Errorf("dataset = %q, want analytics", p.GetDataset())
	}
}

func TestRedshiftProvider_SQLDialect(t *testing.T) {
	p := &RedshiftProvider{}
	if p.SQLDialect() == "" {
		t.Error("SQLDialect should not be empty")
	}
}

func TestExtractFieldValue_Nil(t *testing.T) {
	result := extractFieldValue(nil, types.ColumnMetadata{})
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestExtractFieldValue_Types(t *testing.T) {
	strCol := types.ColumnMetadata{TypeName: aws.String("varchar")}
	tests := []struct {
		name  string
		field types.Field
		col   types.ColumnMetadata
		want  interface{}
	}{
		{"string", &types.FieldMemberStringValue{Value: "hello"}, strCol, "hello"},
		{"long", &types.FieldMemberLongValue{Value: 42}, strCol, int64(42)},
		{"double", &types.FieldMemberDoubleValue{Value: 3.14}, strCol, 3.14},
		{"bool", &types.FieldMemberBooleanValue{Value: true}, strCol, true},
		{"null", &types.FieldMemberIsNull{Value: true}, strCol, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFieldValue(tt.field, tt.col)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractFieldValue_DecimalToFloat(t *testing.T) {
	decCol := types.ColumnMetadata{TypeName: aws.String("numeric(10,2)")}
	got := extractFieldValue(&types.FieldMemberStringValue{Value: "150.75"}, decCol)
	if got != 150.75 {
		t.Errorf("decimal got %v (%T), want 150.75 (float64)", got, got)
	}
}

func TestExtractFieldValue_DecimalZero(t *testing.T) {
	decCol := types.ColumnMetadata{TypeName: aws.String("decimal(10,2)")}
	got := extractFieldValue(&types.FieldMemberStringValue{Value: "0.00"}, decCol)
	if got != 0.0 {
		t.Errorf("decimal zero got %v (%T), want 0.0", got, got)
	}
}

func TestExtractFieldValue_StringNotConverted(t *testing.T) {
	strCol := types.ColumnMetadata{TypeName: aws.String("varchar")}
	got := extractFieldValue(&types.FieldMemberStringValue{Value: "150.75"}, strCol)
	if got != "150.75" {
		t.Errorf("varchar should stay string, got %v (%T)", got, got)
	}
}

// --- Mock client tests ---

func TestMock_Query_Success(t *testing.T) {
	mock := &mockDataAPIClient{
		resultColumns: []types.ColumnMetadata{
			{Name: aws.String("id")},
			{Name: aws.String("name")},
		},
		resultRecords: [][]types.Field{
			{&types.FieldMemberLongValue{Value: 1}, &types.FieldMemberStringValue{Value: "alice"}},
			{&types.FieldMemberLongValue{Value: 2}, &types.FieldMemberStringValue{Value: "bob"}},
		},
	}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	result, err := p.Query(context.Background(), "SELECT id, name FROM users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 2 {
		t.Errorf("columns = %d, want 2", len(result.Columns))
	}
	if len(result.Rows) != 2 {
		t.Errorf("rows = %d, want 2", len(result.Rows))
	}
	if result.Rows[0]["name"] != "alice" {
		t.Errorf("row[0].name = %v", result.Rows[0]["name"])
	}
	if result.Rows[1]["id"] != int64(2) {
		t.Errorf("row[1].id = %v", result.Rows[1]["id"])
	}
	if mock.executedSQL != "SELECT id, name FROM users" {
		t.Errorf("SQL = %q", mock.executedSQL)
	}
}

func TestMock_Query_ExecuteError(t *testing.T) {
	mock := &mockDataAPIClient{executeErr: fmt.Errorf("access denied")}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestMock_Query_Failed(t *testing.T) {
	mock := &mockDataAPIClient{describeStatus: types.StatusStringFailed}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for failed query")
	}
}

func TestMock_Query_Aborted(t *testing.T) {
	mock := &mockDataAPIClient{describeStatus: types.StatusStringAborted}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for aborted query")
	}
}

func TestMock_Query_ResultError(t *testing.T) {
	mock := &mockDataAPIClient{resultErr: fmt.Errorf("result fetch failed")}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	_, err := p.Query(context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Error("expected error for result fetch failure")
	}
}

func TestMock_ListTables(t *testing.T) {
	mock := &mockDataAPIClient{
		tables: []types.TableMember{
			{Name: aws.String("users")},
			{Name: aws.String("orders")},
			{Name: aws.String("pg_catalog")},  // system — should be filtered
			{Name: aws.String("stl_query")},   // system — should be filtered
			{Name: aws.String("svv_tables")},  // system — should be filtered
		},
	}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", dataset: "public", timeout: 10 * time.Second}

	tables, err := p.ListTables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Errorf("tables = %v, want [users, orders]", tables)
	}
}

func TestMock_ListTables_Error(t *testing.T) {
	mock := &mockDataAPIClient{tablesErr: fmt.Errorf("access denied")}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", dataset: "public", timeout: 10 * time.Second}

	_, err := p.ListTables(context.Background())
	if err == nil {
		t.Error("expected error")
	}
}

func TestMock_DescribeTable(t *testing.T) {
	mock := &mockDataAPIClient{
		describeCols: []types.ColumnMetadata{
			{Name: aws.String("id"), TypeName: aws.String("integer"), Nullable: 0},
			{Name: aws.String("name"), TypeName: aws.String("varchar"), Nullable: 1},
			{Name: aws.String("created_at"), TypeName: aws.String("timestamp"), Nullable: 1},
		},
		// Also mock the count query
		resultColumns: []types.ColumnMetadata{{Name: aws.String("cnt")}},
		resultRecords: [][]types.Field{{&types.FieldMemberLongValue{Value: 1000}}},
	}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", dataset: "public", timeout: 10 * time.Second}

	schema, err := p.GetTableSchema(context.Background(), "users")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Name != "users" {
		t.Errorf("name = %q", schema.Name)
	}
	if len(schema.Columns) != 3 {
		t.Fatalf("columns = %d, want 3", len(schema.Columns))
	}
	if schema.Columns[0].Type != "INT64" {
		t.Errorf("col[0].type = %q, want INT64", schema.Columns[0].Type)
	}
	if schema.Columns[1].Type != "STRING" {
		t.Errorf("col[1].type = %q, want STRING", schema.Columns[1].Type)
	}
	if schema.Columns[2].Type != "TIMESTAMP" {
		t.Errorf("col[2].type = %q, want TIMESTAMP", schema.Columns[2].Type)
	}
	if !schema.Columns[1].Nullable {
		t.Error("col[1] should be nullable")
	}
	if schema.RowCount != 1000 {
		t.Errorf("row_count = %d, want 1000", schema.RowCount)
	}
}

func TestMock_Query_Provisioned(t *testing.T) {
	mock := &mockDataAPIClient{
		resultColumns: []types.ColumnMetadata{{Name: aws.String("val")}},
		resultRecords: [][]types.Field{{&types.FieldMemberLongValue{Value: 1}}},
	}
	p := &RedshiftProvider{client: mock, clusterID: "my-cluster", dbUser: "admin", database: "db", timeout: 10 * time.Second}

	result, err := p.Query(context.Background(), "SELECT 1 as val", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("rows = %d", len(result.Rows))
	}
}

func TestMock_EmptyResult(t *testing.T) {
	mock := &mockDataAPIClient{
		resultColumns: []types.ColumnMetadata{{Name: aws.String("id")}},
		resultRecords: [][]types.Field{},
	}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	result, err := p.Query(context.Background(), "SELECT id FROM empty_table", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("rows = %d, want 0", len(result.Rows))
	}
	if len(result.Columns) != 1 {
		t.Errorf("columns = %d, want 1", len(result.Columns))
	}
}

func TestRedshiftProvider_SQLFixPrompt(t *testing.T) {
	p := &RedshiftProvider{}
	prompt := p.SQLFixPrompt()
	// Currently returns empty string (Redshift-specific SQL fix prompt not yet implemented)
	if prompt != "" {
		t.Errorf("SQLFixPrompt() = %q, want empty string", prompt)
	}
}

func TestMock_Query_EmptyColumns(t *testing.T) {
	mock := &mockDataAPIClient{
		resultColumns: []types.ColumnMetadata{},
		resultRecords: [][]types.Field{},
	}
	p := &RedshiftProvider{client: mock, workgroup: "wg", database: "db", timeout: 10 * time.Second}

	result, err := p.Query(context.Background(), "SELECT 1 WHERE false", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 0 {
		t.Errorf("columns = %d, want 0", len(result.Columns))
	}
	if len(result.Rows) != 0 {
		t.Errorf("rows = %d, want 0", len(result.Rows))
	}
}
