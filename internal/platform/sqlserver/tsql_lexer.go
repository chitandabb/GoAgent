package sqlserver

import (
	"errors"
	"strings"
)

type tsqlTokenKind uint8

const (
	tsqlTokenWord tsqlTokenKind = iota + 1
	tsqlTokenIdentifier
	tsqlTokenString
	tsqlTokenNumber
	tsqlTokenVariable
	tsqlTokenTemporary
	tsqlTokenDot
	tsqlTokenComma
	tsqlTokenLeftParen
	tsqlTokenRightParen
	tsqlTokenSemicolon
	tsqlTokenOperator
)

type tsqlToken struct {
	kind  tsqlTokenKind
	text  string
	upper string
}

var errInvalidTSQL = errors.New("invalid T-SQL syntax")

// lexTSQL 只负责安全策略需要的词法边界，不尝试实现完整 T-SQL 语法。
// 注释会被移除，字符串与带引号标识符会保留为单个 Token，避免其中的关键字被误判。
func lexTSQL(input string) ([]tsqlToken, error) {
	tokens := make([]tsqlToken, 0, len(input)/4)
	for i := 0; i < len(input); {
		switch input[i] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			i++
		case '-':
			if i+1 < len(input) && input[i+1] == '-' {
				i += 2
				for i < len(input) && input[i] != '\n' {
					i++
				}
				continue
			}
			tokens = append(tokens, tsqlToken{kind: tsqlTokenOperator, text: "-"})
			i++
		case '/':
			if i+1 < len(input) && input[i+1] == '*' {
				var err error
				i, err = skipNestedBlockComment(input, i)
				if err != nil {
					return nil, err
				}
				continue
			}
			tokens = append(tokens, tsqlToken{kind: tsqlTokenOperator, text: "/"})
			i++
		case '\'':
			next, err := scanDelimited(input, i, '\'', '\'')
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tsqlToken{kind: tsqlTokenString})
			i = next
		case '"':
			value, next, err := scanQuotedIdentifier(input, i, '"', '"')
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, identifierToken(value))
			i = next
		case '[':
			value, next, err := scanQuotedIdentifier(input, i, '[', ']')
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, identifierToken(value))
			i = next
		case '.':
			tokens = append(tokens, tsqlToken{kind: tsqlTokenDot, text: "."})
			i++
		case ',':
			tokens = append(tokens, tsqlToken{kind: tsqlTokenComma, text: ","})
			i++
		case '(':
			tokens = append(tokens, tsqlToken{kind: tsqlTokenLeftParen, text: "("})
			i++
		case ')':
			tokens = append(tokens, tsqlToken{kind: tsqlTokenRightParen, text: ")"})
			i++
		case ';':
			tokens = append(tokens, tsqlToken{kind: tsqlTokenSemicolon, text: ";"})
			i++
		case '@':
			start := i
			i++
			for i < len(input) && isTSQLWordPart(input[i]) {
				i++
			}
			tokens = append(tokens, tsqlToken{kind: tsqlTokenVariable, text: input[start:i]})
		case '#':
			start := i
			i++
			if i < len(input) && input[i] == '#' {
				i++
			}
			for i < len(input) && isTSQLWordPart(input[i]) {
				i++
			}
			tokens = append(tokens, tsqlToken{kind: tsqlTokenTemporary, text: input[start:i]})
		default:
			if isASCIIDigit(input[i]) {
				start := i
				i++
				for i < len(input) && (isASCIIDigit(input[i]) || isASCIIHexLetter(input[i])) {
					i++
				}
				tokens = append(tokens, tsqlToken{kind: tsqlTokenNumber, text: input[start:i]})
				continue
			}
			if isTSQLWordStart(input[i]) {
				start := i
				i++
				for i < len(input) && isTSQLWordPart(input[i]) {
					i++
				}
				word := input[start:i]
				if (word == "N" || word == "n") && i < len(input) && input[i] == '\'' {
					next, err := scanDelimited(input, i, '\'', '\'')
					if err != nil {
						return nil, err
					}
					tokens = append(tokens, tsqlToken{kind: tsqlTokenString})
					i = next
					continue
				}
				tokens = append(tokens, tsqlToken{kind: tsqlTokenWord, text: word, upper: strings.ToUpper(word)})
				continue
			}
			if isTSQLPolicyOperator(input[i]) {
				tokens = append(tokens, tsqlToken{kind: tsqlTokenOperator, text: input[i : i+1]})
				i++
				continue
			}
			return nil, errInvalidTSQL
		}
	}
	return tokens, nil
}

func skipNestedBlockComment(input string, start int) (int, error) {
	depth := 1
	for i := start + 2; i < len(input); {
		if i+1 < len(input) && input[i] == '/' && input[i+1] == '*' {
			depth++
			i += 2
			continue
		}
		if i+1 < len(input) && input[i] == '*' && input[i+1] == '/' {
			depth--
			i += 2
			if depth == 0 {
				return i, nil
			}
			continue
		}
		i++
	}
	return 0, errInvalidTSQL
}

func scanDelimited(input string, start int, delimiter, escaped byte) (int, error) {
	for i := start + 1; i < len(input); i++ {
		if input[i] != delimiter {
			continue
		}
		if i+1 < len(input) && input[i+1] == escaped {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, errInvalidTSQL
}

func scanQuotedIdentifier(input string, start int, opening, closing byte) (string, int, error) {
	var value strings.Builder
	for i := start + 1; i < len(input); i++ {
		if input[i] != closing {
			if input[i] < 0x20 {
				return "", 0, errInvalidTSQL
			}
			value.WriteByte(input[i])
			continue
		}
		if i+1 < len(input) && input[i+1] == closing {
			value.WriteByte(closing)
			i++
			continue
		}
		if value.Len() == 0 {
			return "", 0, errInvalidTSQL
		}
		return value.String(), i + 1, nil
	}
	_ = opening
	return "", 0, errInvalidTSQL
}

func identifierToken(value string) tsqlToken {
	return tsqlToken{kind: tsqlTokenIdentifier, text: value, upper: strings.ToUpper(value)}
}

func isTSQLWordStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isTSQLWordPart(value byte) bool {
	return isTSQLWordStart(value) || isASCIIDigit(value) || value == '$'
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isASCIIHexLetter(value byte) bool {
	return value >= 'A' && value <= 'F' || value >= 'a' && value <= 'f' || value == 'x' || value == 'X'
}

func isTSQLPolicyOperator(value byte) bool {
	return strings.ContainsRune("+*%=<>!~&|^:?", rune(value))
}
