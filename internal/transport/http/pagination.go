package httptransport

type PageQuery struct {
	Page     int `form:"page"`
	PageSize int `form:"pageSize"`
}

// PageData 是列表接口统一使用的分页响应数据。
type PageData[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

// Normalize 将缺省值和越界值收敛到 API 约定的分页范围。
func (p *PageQuery) Normalize() {
	// int 的零值为 0，因此未传 page 和传入非法负数可以统一处理。
	if p.Page < 1 {
		p.Page = 1
	}

	// pageSize 默认 20，最大 100。
	if p.PageSize < 1 {
		p.PageSize = 20
	} else if p.PageSize > 100 {
		p.PageSize = 100
	}
}
