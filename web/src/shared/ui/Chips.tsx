// 筛选 chip:pill 语法，选中态用 2px primary-focus 描边（configurator-option-chip 语义）。
export interface ChipOption {
  value: string
  label: string
}

export function FilterChips({
  options,
  value,
  onChange,
}: {
  options: ChipOption[]
  value: string
  onChange: (v: string) => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      {options.map((o) => {
        const selected = o.value === value
        return (
          <button
            key={o.value}
            type="button"
            onClick={() => onChange(o.value)}
            className={`press focus-ring h-8 rounded-full px-4 text-[13px] ${
              selected
                ? 'bg-ink text-white'
                : 'border border-hairline bg-canvas text-ink-80 hover:bg-pearl'
            }`}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
