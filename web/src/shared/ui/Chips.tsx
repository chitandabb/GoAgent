import * as ToggleGroupPrimitive from '@radix-ui/react-toggle-group'

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
  onChange: (value: string) => void
}) {
  return (
    <ToggleGroupPrimitive.Root
      type="single"
      value={value}
      onValueChange={(next) => next && onChange(next)}
      className="flex flex-wrap items-center gap-2"
    >
      {options.map((option) => (
        <ToggleGroupPrimitive.Item
          key={option.value}
          value={option.value}
          className="press focus-ring h-8 rounded-full border border-hairline bg-canvas px-4 text-[13px] text-ink-80 hover:bg-pearl data-[state=on]:border-ink data-[state=on]:bg-ink data-[state=on]:text-white"
        >
          {option.label}
        </ToggleGroupPrimitive.Item>
      ))}
    </ToggleGroupPrimitive.Root>
  )
}
