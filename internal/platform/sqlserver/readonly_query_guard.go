package sqlserver

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const ReadonlyQueryPolicyVersion = "tsql-readonly-v1"

type QueryRejectionReason string

const (
	QueryRejectedEmpty              QueryRejectionReason = "empty_query"
	QueryRejectedTooLarge           QueryRejectionReason = "query_too_large"
	QueryRejectedInvalidSyntax      QueryRejectionReason = "invalid_syntax"
	QueryRejectedMultipleStatements QueryRejectionReason = "multiple_statements"
	QueryRejectedStatement          QueryRejectionReason = "statement_not_allowed"
	QueryRejectedDangerousKeyword   QueryRejectionReason = "dangerous_keyword"
	QueryRejectedSelectInto         QueryRejectionReason = "select_into"
	QueryRejectedVariable           QueryRejectionReason = "variable_not_allowed"
	QueryRejectedTemporaryObject    QueryRejectionReason = "temporary_object_not_allowed"
	QueryRejectedCrossDatabase      QueryRejectionReason = "cross_database_reference"
	QueryRejectedUnqualifiedObject  QueryRejectionReason = "unqualified_object"
	QueryRejectedSchema             QueryRejectionReason = "schema_not_allowed"
	QueryRejectedUnsupportedSource  QueryRejectionReason = "unsupported_table_source"
	QueryRejectedNoObject           QueryRejectionReason = "no_catalog_object"
	QueryRejectedTooManyObjects     QueryRejectionReason = "too_many_objects"
)

var ErrReadonlyQueryRejected = errors.New("readonly query rejected")

// QueryGuardError 只暴露稳定原因码，不携带原始 SQL、标识符或字面量。
// 上层可记录查询指纹和原因码，但不能把模型生成的敏感 SQL 写入普通日志。
type QueryGuardError struct {
	Reason QueryRejectionReason
}

func (e *QueryGuardError) Error() string {
	return fmt.Sprintf("%s: %s", ErrReadonlyQueryRejected, e.Reason)
}

func (e *QueryGuardError) Unwrap() error {
	return ErrReadonlyQueryRejected
}

type ReadonlyQueryObjectRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

// ReadonlyQueryAnalysis 是安全决策所需的最小模型，不承诺提供完整 T-SQL AST。
type ReadonlyQueryAnalysis struct {
	PolicyVersion string                   `json:"policyVersion"`
	StatementType string                   `json:"statementType"`
	Objects       []ReadonlyQueryObjectRef `json:"objects"`
	HasCTE        bool                     `json:"hasCte"`
	HasUnion      bool                     `json:"hasUnion"`
}

type ReadonlyQueryGuard struct {
	allowedSchemas map[string]struct{}
	maxQueryBytes  int
	maxObjects     int
}

func NewReadonlyQueryGuard(allowedSchemas []string, maxQueryBytes int) (*ReadonlyQueryGuard, error) {
	if maxQueryBytes < 1 {
		return nil, errors.New("readonly query max bytes must be positive")
	}
	allowed := make(map[string]struct{}, len(allowedSchemas))
	for _, schema := range allowedSchemas {
		schema = strings.TrimSpace(schema)
		if !objectIdentifierPattern.MatchString(schema) {
			return nil, errors.New("readonly query allowed schema must be a simple identifier")
		}
		allowed[strings.ToLower(schema)] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("readonly query allowed schemas are empty")
	}
	return &ReadonlyQueryGuard{allowedSchemas: allowed, maxQueryBytes: maxQueryBytes, maxObjects: 64}, nil
}

func (g *ReadonlyQueryGuard) Analyze(query string) (ReadonlyQueryAnalysis, error) {
	if g == nil {
		return ReadonlyQueryAnalysis{}, errors.New("readonly query guard is nil")
	}
	if strings.TrimSpace(query) == "" {
		return ReadonlyQueryAnalysis{}, rejectQuery(QueryRejectedEmpty)
	}
	if len(query) > g.maxQueryBytes {
		return ReadonlyQueryAnalysis{}, rejectQuery(QueryRejectedTooLarge)
	}
	tokens, err := lexTSQL(query)
	if err != nil || len(tokens) == 0 {
		return ReadonlyQueryAnalysis{}, rejectQuery(QueryRejectedInvalidSyntax)
	}
	tokens, err = validateSingleStatement(tokens)
	if err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if err := validateBalancedParentheses(tokens); err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if err := rejectUnsafeTokens(tokens); err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if err := validateSelectChains(tokens); err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if err := validateTableHints(tokens); err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if err := rejectCrossDatabaseExpressions(tokens); err != nil {
		return ReadonlyQueryAnalysis{}, err
	}

	cteNames, hasCTE, err := parseCTENames(tokens)
	if err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	objects, err := g.extractObjectRefs(tokens, cteNames)
	if err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	objects, err = g.appendFunctionRefs(tokens, objects)
	if err != nil {
		return ReadonlyQueryAnalysis{}, err
	}
	if len(objects) == 0 {
		return ReadonlyQueryAnalysis{}, rejectQuery(QueryRejectedNoObject)
	}
	if len(objects) > g.maxObjects {
		return ReadonlyQueryAnalysis{}, rejectQuery(QueryRejectedTooManyObjects)
	}
	return ReadonlyQueryAnalysis{
		PolicyVersion: ReadonlyQueryPolicyVersion,
		StatementType: "SELECT",
		Objects:       objects,
		HasCTE:        hasCTE,
		HasUnion: slices.ContainsFunc(tokens, func(token tsqlToken) bool {
			return token.kind == tsqlTokenWord && token.upper == "UNION"
		}),
	}, nil
}

func rejectQuery(reason QueryRejectionReason) error {
	return &QueryGuardError{Reason: reason}
}

func validateSingleStatement(tokens []tsqlToken) ([]tsqlToken, error) {
	for i, token := range tokens {
		if token.kind != tsqlTokenSemicolon {
			continue
		}
		if i != len(tokens)-1 {
			return nil, rejectQuery(QueryRejectedMultipleStatements)
		}
		tokens = tokens[:i]
	}
	if len(tokens) == 0 {
		return nil, rejectQuery(QueryRejectedEmpty)
	}
	return tokens, nil
}

func validateBalancedParentheses(tokens []tsqlToken) error {
	depth := 0
	for _, token := range tokens {
		switch token.kind {
		case tsqlTokenLeftParen:
			depth++
		case tsqlTokenRightParen:
			depth--
			if depth < 0 {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
		}
	}
	if depth != 0 {
		return rejectQuery(QueryRejectedInvalidSyntax)
	}
	return nil
}

var rejectedTSQLWords = map[string]struct{}{
	"INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {},
	"EXEC": {}, "EXECUTE": {}, "CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {},
	"GRANT": {}, "REVOKE": {}, "DENY": {}, "BACKUP": {}, "RESTORE": {}, "DBCC": {},
	"USE": {}, "KILL": {}, "SHUTDOWN": {}, "WAITFOR": {},
	"DECLARE": {}, "SET": {}, "BEGIN": {}, "COMMIT": {}, "ROLLBACK": {}, "SAVE": {},
	"TRAN": {}, "TRANSACTION": {}, "PRINT": {}, "RAISERROR": {}, "THROW": {},
	"IF": {}, "ELSE": {}, "WHILE": {}, "GOTO": {}, "BREAK": {}, "CONTINUE": {}, "RETURN": {},
	"CURSOR": {}, "BULK": {}, "OPENROWSET": {}, "OPENDATASOURCE": {}, "OPENQUERY": {},
	"OPENXML": {}, "OPENJSON": {}, "FREETEXTTABLE": {}, "CONTAINSTABLE": {}, "CHANGETABLE": {},
	"APPLY": {}, "PIVOT": {}, "UNPIVOT": {}, "TABLESAMPLE": {}, "OPTION": {},
	"UPDLOCK": {}, "XLOCK": {}, "TABLOCKX": {}, "HOLDLOCK": {}, "ROWLOCK": {}, "PAGLOCK": {},
	"GO": {},
}

func rejectUnsafeTokens(tokens []tsqlToken) error {
	if tokens[0].upper != "SELECT" && tokens[0].upper != "WITH" {
		return rejectQuery(QueryRejectedStatement)
	}
	for i, token := range tokens {
		switch token.kind {
		case tsqlTokenVariable:
			return rejectQuery(QueryRejectedVariable)
		case tsqlTokenTemporary:
			return rejectQuery(QueryRejectedTemporaryObject)
		case tsqlTokenWord:
			if token.upper == "INTO" {
				return rejectQuery(QueryRejectedSelectInto)
			}
			if _, rejected := rejectedTSQLWords[token.upper]; rejected {
				return rejectQuery(QueryRejectedDangerousKeyword)
			}
			if token.upper == "NEXT" && i+2 < len(tokens) && tokens[i+1].upper == "VALUE" && tokens[i+2].upper == "FOR" {
				return rejectQuery(QueryRejectedDangerousKeyword)
			}
		}
	}
	return nil
}

// validateSelectChains 拒绝没有分号但在同一查询层级连续拼接的第二条 SELECT。
// UNION/EXCEPT/INTERSECT 是同层出现第二个 SELECT 的唯一合法入口。
func validateSelectChains(tokens []tsqlToken) error {
	depth := 0
	seenSelect := make(map[int]bool)
	expectSelect := make(map[int]bool)
	for _, token := range tokens {
		switch token.kind {
		case tsqlTokenLeftParen:
			depth++
			continue
		case tsqlTokenRightParen:
			if expectSelect[depth] {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
			delete(seenSelect, depth)
			delete(expectSelect, depth)
			depth--
			continue
		}
		if token.kind != tsqlTokenWord {
			continue
		}
		switch token.upper {
		case "SELECT":
			if seenSelect[depth] && !expectSelect[depth] {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
			seenSelect[depth] = true
			expectSelect[depth] = false
		case "UNION", "EXCEPT", "INTERSECT":
			if !seenSelect[depth] || expectSelect[depth] {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
			expectSelect[depth] = true
		case "ALL", "DISTINCT":
			// UNION ALL / UNION DISTINCT 仍在等待后续 SELECT。
		default:
			if expectSelect[depth] {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
		}
	}
	for _, waiting := range expectSelect {
		if waiting {
			return rejectQuery(QueryRejectedInvalidSyntax)
		}
	}
	return nil
}

// validateTableHints 首版只允许单独的 NOLOCK。执行计划和加锁类 Hint 会改变资源与
// 并发行为，不能由模型任意指定；需要其他 Hint 时应通过管理员策略显式扩展。
func validateTableHints(tokens []tsqlToken) error {
	allowedNOLOCK := make(map[int]struct{})
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].kind != tsqlTokenWord || tokens[i].upper != "WITH" || tokens[i+1].kind != tsqlTokenLeftParen {
			continue
		}
		end, err := matchingRightParen(tokens, i+1)
		if err != nil || end != i+3 || tokens[i+2].kind != tsqlTokenWord || tokens[i+2].upper != "NOLOCK" {
			return rejectQuery(QueryRejectedDangerousKeyword)
		}
		allowedNOLOCK[i+2] = struct{}{}
		i = end
	}
	for i, token := range tokens {
		if token.kind != tsqlTokenWord || !isTableHintWord(token.upper) {
			continue
		}
		if _, allowed := allowedNOLOCK[i]; !allowed {
			return rejectQuery(QueryRejectedDangerousKeyword)
		}
	}
	return nil
}

func isTableHintWord(word string) bool {
	switch word {
	case "NOLOCK", "READUNCOMMITTED", "REPEATABLEREAD", "SERIALIZABLE", "READCOMMITTED",
		"READCOMMITTEDLOCK", "TABLOCK", "NOWAIT", "READPAST", "SNAPSHOT", "INDEX",
		"FORCESEEK", "FORCESCAN", "NOEXPAND", "KEEPIDENTITY", "KEEPDEFAULTS",
		"IGNORE_CONSTRAINTS", "IGNORE_TRIGGERS":
		return true
	default:
		return false
	}
}

// rejectCrossDatabaseExpressions 补充 FROM/JOIN 之外的限定名检查。例如
// otherdb.dbo.fn() 不会出现在表来源中，但仍然可能跨库读取或执行对象。
func rejectCrossDatabaseExpressions(tokens []tsqlToken) error {
	for i := 0; i < len(tokens); i++ {
		if !isIdentifierToken(tokens[i]) || i > 0 && tokens[i-1].kind == tsqlTokenDot {
			continue
		}
		parts := 1
		j := i + 1
		star := false
		for j < len(tokens) && tokens[j].kind == tsqlTokenDot {
			if j+1 >= len(tokens) {
				return rejectQuery(QueryRejectedInvalidSyntax)
			}
			if isIdentifierToken(tokens[j+1]) {
				parts++
				j += 2
				continue
			}
			if tokens[j+1].kind == tsqlTokenOperator && tokens[j+1].text == "*" {
				star = true
				j += 2
				break
			}
			return rejectQuery(QueryRejectedInvalidSyntax)
		}
		if parts >= 4 || star && parts >= 3 || parts == 3 && j < len(tokens) && tokens[j].kind == tsqlTokenLeftParen {
			return rejectQuery(QueryRejectedCrossDatabase)
		}
		i = j - 1
	}
	return nil
}

func parseCTENames(tokens []tsqlToken) (map[string]struct{}, bool, error) {
	ctes := make(map[string]struct{})
	if tokens[0].upper == "SELECT" {
		return ctes, false, nil
	}
	if tokens[0].upper != "WITH" {
		return nil, false, rejectQuery(QueryRejectedStatement)
	}

	for i := 1; ; {
		if i >= len(tokens) || !isIdentifierToken(tokens[i]) {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		cteName := strings.ToLower(tokens[i].text)
		if _, exists := ctes[cteName]; exists {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		ctes[cteName] = struct{}{}
		i++

		if i < len(tokens) && tokens[i].kind == tsqlTokenLeftParen {
			end, err := matchingRightParen(tokens, i)
			if err != nil || !validCTEColumnList(tokens[i+1:end]) {
				return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
			}
			i = end + 1
		}
		if i >= len(tokens) || tokens[i].upper != "AS" {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		i++
		if i >= len(tokens) || tokens[i].kind != tsqlTokenLeftParen {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		end, err := matchingRightParen(tokens, i)
		if err != nil || i+1 >= end || tokens[i+1].upper != "SELECT" {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		i = end + 1
		if i < len(tokens) && tokens[i].kind == tsqlTokenComma {
			i++
			continue
		}
		if i >= len(tokens) || tokens[i].upper != "SELECT" {
			return nil, false, rejectQuery(QueryRejectedInvalidSyntax)
		}
		return ctes, true, nil
	}
}

func validCTEColumnList(tokens []tsqlToken) bool {
	if len(tokens) == 0 {
		return false
	}
	expectIdentifier := true
	for _, token := range tokens {
		if expectIdentifier {
			if !isIdentifierToken(token) {
				return false
			}
		} else if token.kind != tsqlTokenComma {
			return false
		}
		expectIdentifier = !expectIdentifier
	}
	return !expectIdentifier
}

func matchingRightParen(tokens []tsqlToken, left int) (int, error) {
	depth := 0
	for i := left; i < len(tokens); i++ {
		switch tokens[i].kind {
		case tsqlTokenLeftParen:
			depth++
		case tsqlTokenRightParen:
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, rejectQuery(QueryRejectedInvalidSyntax)
}

func (g *ReadonlyQueryGuard) extractObjectRefs(
	tokens []tsqlToken,
	cteNames map[string]struct{},
) ([]ReadonlyQueryObjectRef, error) {
	depth := 0
	fromActive := make(map[int]bool)
	expectSource := make(map[int]bool)
	objects := make([]ReadonlyQueryObjectRef, 0, 4)
	seen := make(map[string]struct{})

	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.kind == tsqlTokenLeftParen {
			if expectSource[depth] {
				if i+1 >= len(tokens) || tokens[i+1].upper != "SELECT" {
					return nil, rejectQuery(QueryRejectedUnsupportedSource)
				}
				expectSource[depth] = false
			}
			depth++
			continue
		}
		if token.kind == tsqlTokenRightParen {
			delete(fromActive, depth)
			delete(expectSource, depth)
			depth--
			continue
		}

		if token.kind == tsqlTokenWord {
			if isFromTerminator(token.upper) {
				fromActive[depth] = false
				expectSource[depth] = false
			}
			if token.upper == "FROM" || token.upper == "JOIN" {
				fromActive[depth] = true
				expectSource[depth] = true
				continue
			}
		}

		if expectSource[depth] {
			if !isIdentifierToken(token) {
				return nil, rejectQuery(QueryRejectedUnsupportedSource)
			}
			parts, next, err := readQualifiedName(tokens, i)
			if err != nil {
				return nil, err
			}
			expectSource[depth] = false
			i = next - 1
			if len(parts) == 1 {
				if _, isCTE := cteNames[strings.ToLower(parts[0])]; isCTE {
					continue
				}
				return nil, rejectQuery(QueryRejectedUnqualifiedObject)
			}
			if len(parts) != 2 {
				return nil, rejectQuery(QueryRejectedCrossDatabase)
			}
			if _, allowed := g.allowedSchemas[strings.ToLower(parts[0])]; !allowed {
				return nil, rejectQuery(QueryRejectedSchema)
			}
			key := strings.ToLower(parts[0]) + "." + strings.ToLower(parts[1])
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				objects = append(objects, ReadonlyQueryObjectRef{Schema: parts[0], Name: parts[1]})
			}
			continue
		}

		if token.kind == tsqlTokenComma && fromActive[depth] {
			expectSource[depth] = true
		}
	}
	for _, waiting := range expectSource {
		if waiting {
			return nil, rejectQuery(QueryRejectedUnsupportedSource)
		}
	}
	return objects, nil
}

// appendFunctionRefs 把投影、筛选条件和表来源中的 schema.function(...) 纳入同一
// Catalog 授权清单。SQL Server 内置的一段式函数不在 Catalog 中，由执行权限兜底。
func (g *ReadonlyQueryGuard) appendFunctionRefs(
	tokens []tsqlToken,
	objects []ReadonlyQueryObjectRef,
) ([]ReadonlyQueryObjectRef, error) {
	seen := make(map[string]struct{}, len(objects))
	for _, object := range objects {
		seen[strings.ToLower(object.Schema)+"."+strings.ToLower(object.Name)] = struct{}{}
	}
	for i := 0; i < len(tokens); i++ {
		if !isIdentifierToken(tokens[i]) || i > 0 && tokens[i-1].kind == tsqlTokenDot {
			continue
		}
		parts := []string{tokens[i].text}
		next := i + 1
		for next+1 < len(tokens) && tokens[next].kind == tsqlTokenDot && isIdentifierToken(tokens[next+1]) {
			parts = append(parts, tokens[next+1].text)
			next += 2
		}
		if next < len(tokens) && tokens[next].kind == tsqlTokenDot {
			// .* 已由限定名安全检查验证，它不是函数调用。
			continue
		}
		if next >= len(tokens) || tokens[next].kind != tsqlTokenLeftParen || len(parts) == 1 {
			continue
		}
		if len(parts) != 2 {
			return nil, rejectQuery(QueryRejectedCrossDatabase)
		}
		if _, allowed := g.allowedSchemas[strings.ToLower(parts[0])]; !allowed {
			return nil, rejectQuery(QueryRejectedSchema)
		}
		key := strings.ToLower(parts[0]) + "." + strings.ToLower(parts[1])
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			objects = append(objects, ReadonlyQueryObjectRef{Schema: parts[0], Name: parts[1]})
		}
	}
	return objects, nil
}

func readQualifiedName(tokens []tsqlToken, start int) ([]string, int, error) {
	parts := []string{tokens[start].text}
	i := start + 1
	for i < len(tokens) && tokens[i].kind == tsqlTokenDot {
		if i+1 >= len(tokens) || !isIdentifierToken(tokens[i+1]) {
			return nil, 0, rejectQuery(QueryRejectedInvalidSyntax)
		}
		parts = append(parts, tokens[i+1].text)
		i += 2
	}
	return parts, i, nil
}

func isIdentifierToken(token tsqlToken) bool {
	return token.kind == tsqlTokenWord || token.kind == tsqlTokenIdentifier
}

func isFromTerminator(word string) bool {
	switch word {
	case "WHERE", "GROUP", "HAVING", "ORDER", "UNION", "EXCEPT", "INTERSECT", "OPTION", "FOR", "OFFSET", "FETCH":
		return true
	default:
		return false
	}
}
