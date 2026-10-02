import { useState } from 'react'
import { clearAllTags, recalculateTags } from '../lib/api/tagsRebuild'
import { Button, fieldHintClassName, sectionClassName, sectionTitleClassName } from '../components/ui'
import { t } from '../i18n'

type TagsRebuildSectionProps = {
  onDone: () => void | Promise<void>
}

export function TagsRebuildSection({ onDone }: TagsRebuildSectionProps) {
  const [busy, setBusy] = useState(false)
  const [progress, setProgress] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')

  async function onClear() {
    if (!window.confirm(t('tagsRebuild.confirmClear'))) {
      return
    }
    try {
      setBusy(true)
      setError('')
      setNotice('')
      const deleted = await clearAllTags()
      setNotice(t('tagsRebuild.cleared', { deleted }))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('tagsRebuild.clearError'))
    } finally {
      setBusy(false)
      await onDone()
    }
  }

  async function onRecalculate() {
    if (!window.confirm(t('tagsRebuild.confirm') + '\n\n' + t('tagsRebuild.confirmCost'))) {
      return
    }
    try {
      setBusy(true)
      setError('')
      setNotice('')
      setProgress('')
      const result = await recalculateTags((p) =>
        setProgress(t('tagsRebuild.progress', { done: p.done, total: p.total })),
      )
      setNotice(
        t(result.failed > 0 ? 'tagsRebuild.doneWithFailures' : 'tagsRebuild.done', {
          created: result.created,
          tagged: result.tagged,
          failed: result.failed,
        }),
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : t('tagsRebuild.error'))
    } finally {
      setBusy(false)
      setProgress('')
      await onDone()
    }
  }

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>{t('tagsRebuild.title')}</h2>
      <p className={fieldHintClassName}>{t('tagsRebuild.hint')}</p>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="danger" disabled={busy} onClick={() => void onClear()}>
          {t('tagsRebuild.clearButton')}
        </Button>
        <Button disabled={busy} onClick={() => void onRecalculate()}>
          {busy ? t('tagsPage.working') : t('tagsRebuild.button')}
        </Button>
        {progress && <span className="text-sm text-ink-soft">{progress}</span>}
      </div>
      {notice && <p className="mt-3 text-sm text-ink-soft">{notice}</p>}
      {error && <p className="mt-3 text-sm text-madder">{error}</p>}
    </section>
  )
}
