import { LoaderCircle } from 'lucide-react'
import { cn } from '@/shared/lib/utils'

export function Spinner({ className = '' }: { className?: string }) {
  return (
    <LoaderCircle
      className={cn('size-5 animate-spin text-primary', className)}
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
