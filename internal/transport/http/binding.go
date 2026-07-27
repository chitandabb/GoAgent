package httptransport

import (
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/chitandabb/GoAgent/internal/apperror"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// ============================================================
// 请求绑定与校验辅助层。
//
// Handler 统一通过 BindJSON / BindQuery 获取 DTO，不直接调用
// c.ShouldBindXXX。这样绑定失败的错误翻译只发生在一个地方：
//   - JSON 语法错误、类型不匹配 → 40001 通用消息（原始错误只进日志）；
//   - validator tag 校验失败    → 40001 + data.fields 逐字段安全提示。
// 需要业务数据才能判断的校验（例如附件归属）不在这一层做，
// 由 Service 层返回 42201（apperror.CodeValidationFailed）。
// ============================================================

// BindJSON 绑定并校验 JSON 请求体。
// 失败时已经写入统一错误响应并中止请求，调用方直接 return 即可：
//
//	req, ok := BindJSON[loginRequest](c)
//	if !ok {
//	    return
//	}
func BindJSON[T any](c *gin.Context) (T, bool) {
	registerTagName()
	var dto T
	if err := c.ShouldBindJSON(&dto); err != nil {
		AbortWithError(c, translateBindingError(err))
		return dto, false
	}
	return dto, true
}

// BindQuery 绑定并校验 Query 参数。
func BindQuery[T any](c *gin.Context) (T, bool) {
	registerTagName()
	var dto T
	if err := c.ShouldBindQuery(&dto); err != nil {
		AbortWithError(c, translateBindingError(err))
		return dto, false
	}
	return dto, true
}

// translateBindingError 把 Gin 绑定错误翻译成统一应用错误。
// validator 的字段错误逐条转换为安全提示；其余错误（JSON 语法、类型不匹配）
// 一律返回通用消息，原始错误保留在 Cause 中仅供日志排查。
func translateBindingError(err error) *apperror.Error {
	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		fields := make([]apperror.FieldError, 0, len(validationErrs))
		for _, fieldErr := range validationErrs {
			fields = append(fields, apperror.FieldError{
				Field:  fieldErr.Field(),
				Reason: fieldReason(fieldErr),
			})
		}
		return apperror.NewWithFields(apperror.CodeInvalidArgument, fields)
	}
	return apperror.Wrap(apperror.CodeInvalidArgument, err)
}

// fieldReason 把 validator tag 翻译成安全、可展示的中文提示。
// 只翻译当前项目实际使用的 tag，遇到未登记的 tag 返回通用提示，
// 避免把 validator 的内部描述直接暴露给前端。
func fieldReason(fieldErr validator.FieldError) string {
	switch fieldErr.Tag() {
	case "required":
		return "必填"
	case "min":
		return "不能小于 " + fieldErr.Param()
	case "max":
		return "不能大于 " + fieldErr.Param()
	case "len":
		return "长度必须为 " + fieldErr.Param()
	case "oneof":
		return "取值必须是其中之一 " + fieldErr.Param()
	case "email":
		return "邮箱格式不正确"
	case "uuid":
		return "必须是合法的 UUID"
	default:
		return "格式不正确"
	}
}

var tagNameOnce sync.Once

// registerTagName 让 validator 报错时使用 json/form tag 里的字段名，
// 而不是 Go 结构体字段名。前端拿到的 field 必须和请求 JSON 的键一致，
// 例如 "displayName" 而不是 "DisplayName"。
// Gin 的 validator 引擎是全局单例，因此只注册一次。
func registerTagName() {
	tagNameOnce.Do(func() {
		engine, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		engine.RegisterTagNameFunc(func(field reflect.StructField) string {
			for _, tag := range []string{"json", "form", "uri"} {
				name := strings.SplitN(field.Tag.Get(tag), ",", 2)[0]
				if name == "-" {
					return ""
				}
				if name != "" {
					return name
				}
			}
			return field.Name
		})
	})
}
