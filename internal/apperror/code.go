// Package apperror 定义应用内部统一使用的错误码和错误类型。
//
// 该包不依赖 Gin 或 net/http，因此后续 Service、Repository 和其他入口
// 都可以复用同一套错误语义。
package apperror

// Code 是应用错误码。
//
// Go 没有 Java enum，通常使用自定义类型配合 const 表达枚举。
type Code int

const (
	// CodeSuccess 表示请求处理成功。
	CodeSuccess Code = 0

	// 4xxxx 表示客户端请求存在问题。
	CodeInvalidArgument     Code = 40001
	CodeUnauthorized        Code = 40101
	CodeForbidden           Code = 40301
	CodeNotFound            Code = 40401
	CodeMethodNotAllowed    Code = 40501
	CodeConflict            Code = 40901
	CodeIdempotencyConflict Code = 40911
	CodeTaskStateConflict   Code = 40921
	CodeSourceChanged       Code = 40923
	// CodeValidationFailed 表示请求格式正确但业务校验失败。
	// 与 CodeInvalidArgument 的分界：40001 由绑定层产生（格式、类型、基础规则），
	// 42201 由 Service 层产生（需要业务数据才能判断，例如附件不属于当前用户）。
	CodeValidationFailed Code = 42201

	// 5xxxx 表示服务端或外部依赖发生问题。
	CodeInternal              Code = 50000
	CodeDependencyUnavailable Code = 50301
)

var messages = map[Code]string{
	CodeSuccess:               "success",
	CodeInvalidArgument:       "请求参数错误",
	CodeUnauthorized:          "未登录或登录状态已失效",
	CodeForbidden:             "无权执行该操作",
	CodeNotFound:              "请求的资源不存在",
	CodeMethodNotAllowed:      "请求方法不支持",
	CodeConflict:              "资源状态冲突",
	CodeIdempotencyConflict:   "幂等键对应的请求内容不一致",
	CodeTaskStateConflict:     "任务当前状态不允许此操作",
	CodeSourceChanged:         "外部工单已发生变化，请刷新后重试",
	CodeValidationFailed:      "业务参数校验失败",
	CodeInternal:              "服务器内部错误",
	CodeDependencyUnavailable: "外部依赖暂时不可用",
}

// Message 返回错误码对应的默认用户提示。
func (c Code) Message() string {
	if message, ok := messages[c]; ok {
		return message
	}
	return messages[CodeInternal]
}
