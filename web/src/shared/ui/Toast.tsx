import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
} from 'react'

interface ToastItem {
  id: number
  kind: 'success' | 'error'
  text: string
}

interface ToastApi {
  success: (text: string) => void
  error: (text: string) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const nextId = useRef(1)

  const push = useCallback((kind: ToastItem['kind'], text: string) => {
    const id = nextId.current++
    setItems((prev) => [...prev, { id, kind, text }])
    window.setTimeout(() => {
      setItems((prev) => prev.filter((t) => t.id !== id))
    }, 4000)
  }, [])

  const api = useMemo<ToastApi>(
    () => ({
      success: (text) => push('success', text),
      error: (text) => push('error', text),
    }),
    [push],
  )

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed inset-x-0 bottom-8 z-[60] flex flex-col items-center gap-2 print:hidden">
        {items.map((t) => (
          <div
            key={t.id}
            className="frosted pointer-events-auto flex items-center gap-2.5 rounded-full border border-hairline px-5 py-2.5"
          >
            <i
              className={`size-2 shrink-0 rounded-full ${
                t.kind === 'success' ? 'bg-ok' : 'bg-danger'
              }`}
            />
            <span className="text-[13px] text-ink">{t.text}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast must be used within ToastProvider')
  return ctx
}
