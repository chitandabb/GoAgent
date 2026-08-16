import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Dialog } from './Dialog'
import { Spinner } from './Spinner'
import { getAttachmentPreview } from '@/shared/api/attachments'
import { getKnowledgeCitation } from '@/shared/api/knowledge'
import type { AttachmentPreviewElement } from '@/shared/api/m1-types'

export type PreviewTarget =
  | { kind: 'attachment'; conversationId: string; attachmentId: string; name: string }
  | { kind: 'knowledge'; chunkId: string }

type PreviewData =
  | Awaited<ReturnType<typeof getAttachmentPreview>>
  | Awaited<ReturnType<typeof getKnowledgeCitation>>

function isAttachmentPreview(data: PreviewData): data is Awaited<ReturnType<typeof getAttachmentPreview>> {
  return 'elements' in data
}

function ElementBlock({ element }: { element: AttachmentPreviewElement }) {
  return (
    <div className="rounded-utility border border-hairline bg-tile p-3.5">
      <p className="mb-1.5 flex items-center gap-2 text-[11px] text-ink-48">
        <span>
          {element.elementType}
          {element.pageNumber !== undefined ? ` · 第 ${element.pageNumber} 页` : ''}
        </span>
        {element.sectionPath && element.sectionPath.length > 0 && (
          <span className="truncate">{element.sectionPath.join(' / ')}</span>
        )}
      </p>
      <p className="whitespace-pre-wrap text-[12px] leading-[1.75] text-ink-80">{element.contentText}</p>
    </div>
  )
}

export function AttachmentPreviewDialog({
  preview,
  onClose,
}: {
  preview: PreviewTarget | null
  onClose: () => void
}) {
  const title = preview?.kind === 'attachment' ? preview.name : preview?.kind === 'knowledge' ? '知识库引用' : ''

  const data = useQuery<PreviewData>({
    queryKey: preview
      ? preview.kind === 'attachment'
        ? ['attachment-preview', preview.conversationId, preview.attachmentId]
        : ['knowledge-citation', preview.chunkId]
      : ['preview-none'],
    queryFn: () =>
      preview
        ? preview.kind === 'attachment'
          ? getAttachmentPreview(preview.conversationId, preview.attachmentId)
          : getKnowledgeCitation(preview.chunkId)
        : Promise.reject(new Error('no preview')),
    enabled: preview !== null,
    retry: false,
  })

  return (
    <Dialog open={preview !== null} title={title || '预览'} onClose={onClose} width="w-[680px]">
      {preview === null ? null : data.isPending ? (
        <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-ink-48">
          <Spinner /> 正在加载预览…
        </div>
      ) : data.isError ? (
        <p className="py-6 text-center text-[13px] text-danger">
          {data.error instanceof Error ? data.error.message : '预览加载失败'}
        </p>
      ) : data.data ? (
        <div className="flex flex-col gap-2.5">
          {isAttachmentPreview(data.data) ? (
            <>
              {data.data.elements.length > 0 ? (
                data.data.elements.map((element) => <ElementBlock key={element.index} element={element} />)
              ) : (
                <p className="py-6 text-center text-[12px] text-ink-48">
                  该内容没有可展示的文本片段
                </p>
              )}
              {data.data.visualAssetCount > 0 && (
                <p className="text-[12px] text-ink-48">
                  另有 {data.data.visualAssetCount} 张图片与版面元素未在文本预览中展示
                </p>
              )}
              {data.data.truncated && (
                <p className="text-[12px] text-warn">内容较长，预览仅展示开头部分</p>
              )}
            </>
          ) : (
            <div>
              <p className="mb-1 text-[11px] font-semibold text-ink-48">{data.data.title}</p>
              <p className="whitespace-pre-wrap rounded-utility border border-hairline bg-tile p-3.5 text-[12px] leading-[1.75] text-ink-80">
                {data.data.contentText}
              </p>
            </div>
          )}
          <p className="mt-1 text-[11px] leading-[1.6] text-ink-48">
            预览内容由服务端解析并脱敏，原始文件保持私有，页面不接触对象存储凭证。
          </p>
        </div>
      ) : null}
    </Dialog>
  )
}

/** 可点击的引用/附件名：点击打开预览。配合本地 state 使用。 */
export function useAttachmentPreview() {
  const [preview, setPreview] = useState<PreviewTarget | null>(null)
  return {
    preview,
    openPreview: setPreview,
    closePreview: () => setPreview(null),
  }
}
