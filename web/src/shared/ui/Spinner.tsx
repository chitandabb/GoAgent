export function Spinner({ className = '' }: { className?: string }) {
  return (
    <span
      className={`inline-block size-5 animate-spin rounded-full border-2 border-hairline border-t-primary ${className}`}
      role="status"
      aria-label="加载中"
    />
  )
}

export function PageLoading() {
  return (
    <div className="flex justify-center py-24">
      <Spinner className="size-6" />
    </div>
  )
}
