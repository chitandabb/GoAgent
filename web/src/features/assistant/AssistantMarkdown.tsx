import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

const components: Components = {
  h1: ({ children }) => <h1 className="mb-2 mt-4 text-[18px] font-semibold leading-[1.45] text-ink">{children}</h1>,
  h2: ({ children }) => <h2 className="mb-2 mt-4 text-[16px] font-semibold leading-[1.5] text-ink">{children}</h2>,
  h3: ({ children }) => <h3 className="mb-1.5 mt-3 text-[14px] font-semibold leading-[1.55] text-ink">{children}</h3>,
  p: ({ children }) => <p className="my-2 first:mt-0 last:mb-0">{children}</p>,
  ul: ({ children }) => <ul className="my-2 list-disc space-y-1 pl-5 marker:text-primary">{children}</ul>,
  ol: ({ children }) => <ol className="my-2 list-decimal space-y-1 pl-5 marker:font-semibold marker:text-primary">{children}</ol>,
  li: ({ children }) => <li className="pl-0.5">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="my-3 border-l-2 border-primary bg-parchment px-3 py-2 text-ink-80">
      {children}
    </blockquote>
  ),
  a: ({ children, href }) => (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      className="focus-ring font-medium text-primary underline decoration-primary/35 underline-offset-2 hover:decoration-primary"
    >
      {children}
    </a>
  ),
  pre: ({ children }) => (
    <pre className="my-3 overflow-x-auto rounded-utility border border-hairline bg-pearl p-3 font-mono text-[12px] leading-[1.65] text-ink">
      {children}
    </pre>
  ),
  code: ({ children, className, ...props }) => {
    const block = Boolean(className?.startsWith('language-')) || String(children).includes('\n')
    return (
      <code
        className={
          block
            ? `font-mono ${className ?? ''}`
            : 'rounded-[4px] bg-code px-1.5 py-0.5 font-mono text-[0.88em] text-ink'
        }
        {...props}
      >
        {children}
      </code>
    )
  },
  table: ({ children }) => (
    <div className="my-3 overflow-x-auto rounded-utility border border-hairline">
      <table className="w-full border-collapse text-left text-[12px]">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-parchment text-ink">{children}</thead>,
  th: ({ children }) => <th className="border-b border-hairline px-3 py-2 font-semibold">{children}</th>,
  td: ({ children }) => <td className="border-b border-divider px-3 py-2 align-top last:border-b-0">{children}</td>,
  hr: () => <hr className="my-4 border-0 border-t border-divider" />,
  input: (props) => <input {...props} className="mr-1.5 accent-primary" />,
}

export function AssistantMarkdown({ content, streaming = false }: { content: string; streaming?: boolean }) {
  return (
    <div className="text-[14px] leading-[1.7] text-ink">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components} skipHtml>
        {content}
      </ReactMarkdown>
      {streaming && (
        <span
          className="ml-0.5 inline-block h-[15px] w-[7px] translate-y-[2px] animate-pulse rounded-[1px] bg-primary"
          aria-hidden="true"
        />
      )}
    </div>
  )
}
