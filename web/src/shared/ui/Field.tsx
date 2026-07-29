import type { InputHTMLAttributes, ReactElement, ReactNode, TextareaHTMLAttributes } from 'react'
import { Children, isValidElement } from 'react'
import * as LabelPrimitive from '@radix-ui/react-label'
import * as SelectPrimitive from '@radix-ui/react-select'
import { Check, ChevronDown, Search } from 'lucide-react'
import { cn } from '@/shared/lib/utils'

export function FieldLabel({
  children,
  hint,
  htmlFor,
}: {
  children: ReactNode
  hint?: string
  htmlFor?: string
}) {
  return (
    <div className="mb-1.5 flex items-baseline justify-between">
      <LabelPrimitive.Root htmlFor={htmlFor} className="text-[13px] font-semibold text-ink-80">
        {children}
      </LabelPrimitive.Root>
      {hint && <span className="text-[12px] text-ink-48">{hint}</span>}
    </div>
  )
}

const controlCls =
  'focus-ring w-full rounded-[12px] border border-hairline bg-canvas px-3.5 text-[14px] text-ink placeholder:text-ink-48 disabled:cursor-not-allowed disabled:opacity-45'

export function TextInput({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input data-slot="input" className={cn(controlCls, 'h-10', className)} {...props} />
}

export function TextArea({
  className,
  ...props
}: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(controlCls, 'min-h-24 py-2.5 leading-[1.6]', className)}
      {...props}
    />
  )
}

interface SelectProps {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  children: ReactNode
  className?: string
  disabled?: boolean
  placeholder?: string
  'aria-label'?: string
}

export function Select({
  value,
  defaultValue,
  onValueChange,
  children,
  className,
  disabled,
  placeholder = '请选择',
  'aria-label': ariaLabel,
}: SelectProps) {
  const options = Children.toArray(children).filter(
    (child): child is ReactElement<{ value: string; children: ReactNode; disabled?: boolean }> =>
      isValidElement(child) && child.type === 'option',
  )

  return (
    <SelectPrimitive.Root
      value={value}
      defaultValue={defaultValue}
      onValueChange={onValueChange}
      disabled={disabled}
    >
      <SelectPrimitive.Trigger
        data-slot="select-trigger"
        aria-label={ariaLabel}
        className={cn(
          controlCls,
          'flex h-10 items-center justify-between gap-2 text-left disabled:pointer-events-none [&>span]:truncate',
          className,
        )}
      >
        <SelectPrimitive.Value placeholder={placeholder} />
        <SelectPrimitive.Icon asChild>
          <ChevronDown className="size-4 shrink-0 text-ink-48" />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          position="popper"
          sideOffset={6}
          className="z-[70] min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-capsule border border-hairline bg-canvas p-1.5 text-ink shadow-none"
        >
          <SelectPrimitive.Viewport>
            {options.map((option) => (
              <SelectPrimitive.Item
                key={option.props.value}
                value={option.props.value}
                disabled={option.props.disabled}
                className="focus:bg-pearl relative flex h-8 cursor-default select-none items-center rounded-utility py-1.5 pl-8 pr-3 text-[13px] outline-none data-[disabled]:pointer-events-none data-[disabled]:opacity-45"
              >
                <span className="absolute left-2.5 flex size-4 items-center justify-center">
                  <SelectPrimitive.ItemIndicator>
                    <Check className="size-3.5 text-primary" />
                  </SelectPrimitive.ItemIndicator>
                </span>
                <SelectPrimitive.ItemText>{option.props.children}</SelectPrimitive.ItemText>
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  )
}

export function SearchInput({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <div className={cn('relative', className)}>
      <Search className="pointer-events-none absolute left-4 top-1/2 size-3.5 -translate-y-1/2 text-ink-48" />
      <input
        type="search"
        className="focus-ring h-10 w-full rounded-full border border-hairline bg-canvas pl-10 pr-4 text-[14px] text-ink placeholder:text-ink-48"
        {...props}
      />
    </div>
  )
}
