import { CircleCheck, CircleX } from 'lucide-react'
import { Toaster, toast } from 'sonner'

interface ToastApi {
  success: (text: string) => void
  error: (text: string) => void
}

const toastApi: ToastApi = {
  success: (text) => toast.success(text),
  error: (text) => toast.error(text),
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  return (
    <>
      {children}
      <Toaster
        position="bottom-center"
        duration={4000}
        icons={{
          success: <CircleCheck className="size-4 text-ok" />,
          error: <CircleX className="size-4 text-danger" />,
        }}
        toastOptions={{
          unstyled: true,
          classNames: {
            toast:
              'frosted flex items-center gap-2.5 rounded-full border border-hairline px-5 py-2.5 text-[13px] text-ink',
          },
        }}
      />
    </>
  )
}

export function useToast(): ToastApi {
  return toastApi
}
