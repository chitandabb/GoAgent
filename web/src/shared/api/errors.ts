export interface ApiFieldError {
  field: string
  reason: string
}

export class ApiError extends Error {
  constructor(
    public code: number,
    message: string,
    public status?: number,
    public requestId?: string,
    public fields: ApiFieldError[] = [],
  ) {
    super(message)
    this.name = 'ApiError'
  }
}
