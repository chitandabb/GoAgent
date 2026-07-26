import type {
  InputHTMLAttributes,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'

export function FieldLabel({
  children,
  hint,
}: {
  children: React.ReactNode
  hint?: string
}) {
  return (
    <div className="mb-1.5 flex items-baseline justify-between">
      <label className="text-[13px] font-semibold text-ink-80">{children}</label>
      {hint && <span className="text-[12px] text-ink-48">{hint}</span>}
    </div>
  )
}

const controlCls =
  'focus-ring w-full rounded-[12px] border border-hairline bg-canvas px-3.5 text-[14px] text-ink placeholder:text-ink-48'

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
  const { className = '', ...rest } = props
  return <input className={`${controlCls} h-10 ${className}`} {...rest} />
}

export function TextArea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const { className = '', ...rest } = props
  return (
    <textarea className={`${controlCls} min-h-24 py-2.5 leading-[1.6] ${className}`} {...rest} />
  )
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  const { className = '', ...rest } = props
  return <select className={`${controlCls} h-10 ${className}`} {...rest} />
}

/** 规范：搜索框是 pill 形，与操作语法一致 */
export function SearchInput(props: InputHTMLAttributes<HTMLInputElement>) {
  const { className = '', ...rest } = props
  return (
    <div className={`relative ${className}`}>
      <svg
        className="pointer-events-none absolute left-4 top-1/2 size-3.5 -translate-y-1/2 text-ink-48"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
      >
        <circle cx="7" cy="7" r="4.5" />
        <path d="m10.5 10.5 3 3" strokeLinecap="round" />
      </svg>
      <input
        type="search"
        className="focus-ring h-10 w-full rounded-full border border-hairline bg-canvas pl-10 pr-4 text-[14px] text-ink"
        {...rest}
      />
    </div>
  )
}
