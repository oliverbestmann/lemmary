import { t } from '../../i18n'
import { apiFetch, pollJob, type JobProgress } from '../apiClient'
import { notifyDocumentsChanged } from '../documentEvents'

export type TagRebuildResult = {
  documents: number
  tagged: number
  failed: number
  created: number
  errors?: string[]
}

const rebuildTimeoutMs = 12 * 60 * 60 * 1000

export async function clearAllTags(): Promise<number> {
  const result = await apiFetch<{ deleted?: number }>('/api/app/tags/clear', {
    method: 'POST',
    fallbackError: t('tagsRebuild.clearError'),
  })
  notifyDocumentsChanged()
  return result.deleted ?? 0
}

/**
 * Asks the model to tag each document again, creating the tags it needs and
 * only ever adding. One model call per document.
 */
export async function recalculateTags(onProgress?: (progress: JobProgress) => void): Promise<TagRebuildResult> {
  const start = await apiFetch<{ job_id?: string }>('/api/app/tags/rebuild', {
    method: 'POST',
    fallbackError: t('tagsRebuild.startFailed'),
  })
  if (!start.job_id) {
    throw new Error(t('tagsRebuild.startFailed'))
  }
  try {
    return await pollJob<TagRebuildResult>(
      `/api/app/tags/rebuild/status?job_id=${encodeURIComponent(start.job_id)}`,
      { onProgress, label: t('tagsRebuild.jobLabel'), timeoutMs: rebuildTimeoutMs },
    )
  } finally {
    notifyDocumentsChanged()
  }
}
