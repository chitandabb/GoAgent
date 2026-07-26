import { Link } from 'react-router'

export function NotFoundPage() {
  return (
    <div className="flex flex-col items-center gap-3 py-28 text-center">
      <p className="text-[56px] font-semibold leading-none tracking-[-0.5px] text-ink">
        404
      </p>
      <p className="text-[15px] text-ink-48">页面不存在，或你没有访问权限。</p>
      <Link
        to="/cases"
        className="press mt-3 inline-flex h-10 items-center rounded-full bg-primary px-[22px] text-[14px] text-white hover:bg-primary-focus"
      >
        返回外部工单
      </Link>
    </div>
  )
}
