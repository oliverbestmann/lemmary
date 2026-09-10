import { type SubmitEvent, useEffect, useRef, useState } from 'react'
import { getMe } from '../lib/auth'
import {
  deletePasskey,
  listPasskeys,
  passkeyDateLabel,
  registerPasskey,
  renamePasskey,
  type Passkey,
} from '../lib/api/passkeys'
import { defaultPasskeyName, passkeysSupported, passkeyUnavailableHint } from '../lib/webauthn'
import { createMCPToken, getMCPStatus } from '../lib/api/mcp'
import { mcpSnippets } from '../lib/mcpSnippets'
import {
  apiTokenDateLabel,
  createApiToken,
  deleteApiToken,
  listApiTokens,
  type ApiToken,
} from '../lib/api/api-tokens'
import { useAsync } from '../hooks/useAsync'
import { getLimits } from '../lib/api/limits'
import { LimitsUsage } from '../components/LimitsUsage'
import {
  Button,
  DocsLink,
  fieldHintClassName,
  inputClassName,
  labelClassName,
  labelTextClassName,
  sectionClassName,
  sectionTitleClassName,
  selectClassName,
} from '../components/ui'
import { docsUrl, t, tNode } from '../i18n'

function SignedInSection() {
  const { data: me } = useAsync(getMe, [])

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>{t('account.signedInAs')}</h2>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-ink">{me?.email || '...'}</span>
        {me?.is_admin && (
          <span className="border border-line-strong px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-ink-soft">
            {t('account.admin')}
          </span>
        )}
      </div>
    </section>
  )
}

type PasskeyRowProps = {
  passkey: Passkey
  busy: boolean
  onRename: (id: string, name: string) => Promise<void>
  onDelete: (passkey: Passkey) => Promise<void>
}

function PasskeyRow({ passkey, busy, onRename, onDelete }: PasskeyRowProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(passkey.name)

  async function onSubmit(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = draft.trim()
    if (!name) {
      return
    }
    await onRename(passkey.id, name)
    setEditing(false)
  }

  return (
    <li className="flex flex-wrap items-center justify-between gap-2 rounded-xs border border-line bg-bright px-3 py-2">
      {editing ? (
        <form className="flex flex-1 flex-wrap items-center gap-2" onSubmit={onSubmit}>
          <input
            aria-label={t('account.passkeyName')}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            className={`${inputClassName} max-w-xs flex-1`}
          />
          <Button type="submit" size="xs" disabled={busy}>
            {t('common.save')}
          </Button>
          <Button
            size="xs"
            variant="secondary"
            disabled={busy}
            onClick={() => {
              setDraft(passkey.name)
              setEditing(false)
            }}
          >
            {t('common.cancel')}
          </Button>
        </form>
      ) : (
        <>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium text-ink">{passkey.name}</p>
            <p className="text-xs text-ink-soft">
              {t('account.passkeyDates', {
                created: passkeyDateLabel(passkey.created),
                used: passkeyDateLabel(passkey.last_used),
              })}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="xs"
              variant="secondary"
              disabled={busy}
              onClick={() => {
                setDraft(passkey.name)
                setEditing(true)
              }}
            >
              {t('account.rename')}
            </Button>
            <Button
              size="xs"
              variant="danger"
              disabled={busy}
              onClick={() => void onDelete(passkey)}
            >
              {t('common.delete')}
            </Button>
          </div>
        </>
      )}
    </li>
  )
}

function PasskeysSection() {
  const { data: passkeys, error: loadError, reload } = useAsync(listPasskeys, [])
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  // Only enrolling needs a working ceremony: listing and deleting must keep
  // working on an address that cannot run one, or a passkey enrolled over HTTPS
  // could never be cleaned up from a plain-HTTP LAN address.
  const canEnroll = passkeysSupported()
  const rows = passkeys ?? []

  function openAddForm() {
    setName(defaultPasskeyName())
    setError('')
    setNotice('')
    setAdding(true)
  }

  async function onAdd(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault()
    try {
      setBusy(true)
      setError('')
      setNotice('')
      const created = await registerPasskey(name.trim() || defaultPasskeyName())
      await reload()
      setAdding(false)
      setNotice(t('account.passkeyAdded', { name: created.name }))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('account.addFailed'))
    } finally {
      setBusy(false)
    }
  }

  async function onRename(id: string, nextName: string) {
    try {
      setBusy(true)
      setError('')
      setNotice('')
      await renamePasskey(id, nextName)
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('account.renameFailed'))
    } finally {
      setBusy(false)
    }
  }

  async function onDelete(passkey: Passkey) {
    const lastOne = rows.length === 1
    const warning = lastOne
      ? t('account.removeLastConfirm', { name: passkey.name })
      : t('account.removeConfirm', { name: passkey.name })
    if (!window.confirm(warning)) {
      return
    }
    try {
      setBusy(true)
      setError('')
      setNotice('')
      await deletePasskey(passkey.id)
      await reload()
      setNotice(t('account.passkeyRemoved', { name: passkey.name }))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('account.removeFailed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>{t('account.passkeys')}</h2>
      <p className={`${fieldHintClassName} mb-4`}>
        {t('account.passkeysHint')}
      </p>

      {loadError && <p className="mb-3 text-sm text-madder">{loadError}</p>}
      {error && <p className="mb-3 text-sm text-madder">{error}</p>}
      {notice && <p className="mb-3 text-sm text-ink-soft">{notice}</p>}

      {rows.length === 0 ? (
        <p className="mb-4 text-sm text-ink-soft">{t('account.noPasskeys')}</p>
      ) : (
        <ul className="mb-4 flex flex-col gap-2">
          {rows.map((passkey) => (
            <PasskeyRow
              key={passkey.id}
              passkey={passkey}
              busy={busy}
              onRename={onRename}
              onDelete={onDelete}
            />
          ))}
        </ul>
      )}

      {!canEnroll && <p className="text-sm text-amber-800">{passkeyUnavailableHint()}</p>}

      {canEnroll && !adding && (
        <Button onClick={openAddForm} disabled={busy}>
          {t('account.addPasskey')}
        </Button>
      )}

      {canEnroll && adding && (
        <form className="flex flex-col gap-3" onSubmit={onAdd}>
          <label className={labelClassName}>
            <span className={labelTextClassName}>{t('account.name')}</span>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              className={`${inputClassName} max-w-sm`}
            />
          </label>
          <p className={fieldHintClassName}>{t('account.nameHint')}</p>
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy}>
              {busy ? t('account.waiting') : t('account.createPasskey')}
            </Button>
            <Button variant="secondary" disabled={busy} onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
          </div>
        </form>
      )}
    </section>
  )
}

const tokenPlaceholder = '<token>'

function AgentsSection() {
  const { data: status, error: loadError } = useAsync(getMCPStatus, [])
  const [token, setToken] = useState('')
  const [agent, setAgent] = useState('claude-code')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)

  const snippets = status?.enabled ? mcpSnippets(status.url, token || tokenPlaceholder) : []
  const current = snippets.find((snippet) => snippet.id === agent) ?? snippets[0]

  async function onCreateToken() {
    setBusy(true)
    setError('')
    setCopied(false)
    try {
      setToken(await createMCPToken())
    } catch (err) {
      setError(err instanceof Error ? err.message : t('account.tokenFailed'))
    } finally {
      setBusy(false)
    }
  }

  async function onCopy() {
    if (!current) {
      return
    }
    try {
      await navigator.clipboard.writeText(current.text)
      setCopied(true)
    } catch {
      setError(t('account.copyFailed'))
    }
  }

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>{t('account.agents')}</h2>
      <p className={`${fieldHintClassName} mb-4`}>
        {tNode('account.agentsHint', {
          link: <DocsLink href={docsUrl('mcp.html')}>{t('account.mcpLink')}</DocsLink>,
        })}
      </p>

      {loadError && <p className="mb-3 text-sm text-madder">{loadError}</p>}
      {status && !status.enabled && (
        <p className="text-sm text-ink-soft">
          {tNode('account.mcpOff', { env: <code>MCP_ENABLED=0</code> })}
        </p>
      )}

      {status?.enabled && current && (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={onCreateToken} disabled={busy}>
              {token ? t('account.createAnotherToken') : t('account.createToken')}
            </Button>
            <span className={fieldHintClassName}>
              {token
                ? t('account.tokenShown')
                : t('account.tokenHint')}
            </span>
          </div>
          {error && <p className="text-sm text-madder">{error}</p>}

          <label className={labelClassName}>
            <span className={labelTextClassName}>{t('account.agent')}</span>
            <select
              value={current.id}
              onChange={(event) => {
                setAgent(event.target.value)
                setCopied(false)
              }}
              className={`${selectClassName} max-w-xs`}
            >
              {snippets.map((snippet) => (
                <option key={snippet.id} value={snippet.id}>
                  {snippet.name}
                </option>
              ))}
            </select>
          </label>

          <div>
            <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
              <span className={fieldHintClassName}>{current.where}</span>
              <Button size="xs" variant="secondary" onClick={onCopy}>
                {copied ? t('account.copied') : t('account.copy')}
              </Button>
            </div>
            <pre
              data-testid="mcp-snippet"
              className="overflow-x-auto rounded-xs border border-line bg-bright p-3 text-xs text-ink"
            >
              {current.text}
            </pre>
            {!token && (
              <p className={`${fieldHintClassName} mt-1`}>
                {tNode('account.replaceToken', { token: <code>{tokenPlaceholder}</code> })}
              </p>
            )}
          </div>
        </div>
      )}
    </section>
  )
}

function InstanceLimitsSection() {
  const { data: limits } = useAsync(getLimits, [])
  if (!limits || !(limits.enforced || (limits.misconfigured?.length ?? 0) > 0)) return null

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>{t('account.limitsTitle')}</h2>
      <p className="text-xs text-ink-soft">{t('account.limitsHint')}</p>
      {limits.enforced ? (
        <LimitsUsage limits={limits} className="mt-4 border-0 bg-transparent p-0" />
      ) : (
        <p className="mt-4 text-xs text-ink-soft">{t('account.noLimits')}</p>
      )}
      {(limits.misconfigured?.length ?? 0) > 0 && (
        <p className="mt-4 text-sm text-madder">
          {t('account.limitsMisconfigured', { names: limits.misconfigured?.join(', ') ?? '' })}
        </p>
      )}
      {limits.enforced && (
        <p className="mt-4 text-xs text-ink-faint">{t('account.limitsUpgrade')}</p>
      )}
    </section>
  )
}

type NewTokenDialogProps = {
  token: string | null
  onClose: () => void
}

/**
 * Shows a freshly minted token exactly once: the server never sends the raw
 * value again, only its hash. A modal rather than an inline banner, so it
 * cannot be scrolled past and lost before it is copied.
 */
function NewTokenDialog({ token, onClose }: NewTokenDialogProps) {
  const ref = useRef<HTMLDialogElement>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (!token) {
      dialog.close()
      return
    }
    dialog.showModal()
  }, [token])

  async function onCopy() {
    if (!token) return
    try {
      await navigator.clipboard.writeText(token)
      setCopied(true)
    } catch {
      // Clipboard access can be denied; the token is still selectable text.
    }
  }

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      className="m-auto max-w-lg border border-line bg-surface p-5 text-ink backdrop:bg-ink/40"
    >
      <h3 className="font-display text-lg font-semibold text-ink">Your new API token</h3>
      <p className="mt-1 text-sm text-ink-soft">
        Copy it now — it will not be shown again. If you lose it, delete this token and create a
        new one.
      </p>
      <code className="mt-4 block break-all rounded-xs border border-line-strong bg-bright px-3 py-2 text-sm text-ink">
        {token}
      </code>
      <div className="mt-4 flex items-center gap-2">
        <Button onClick={() => void onCopy()}>{copied ? 'Copied' : 'Copy to clipboard'}</Button>
        <Button variant="secondary" onClick={onClose}>
          Done
        </Button>
      </div>
    </dialog>
  )
}

type ApiTokenRowProps = {
  token: ApiToken
  busy: boolean
  onDelete: (token: ApiToken) => Promise<void>
}

function ApiTokenRow({ token, busy, onDelete }: ApiTokenRowProps) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 rounded-xs border border-line bg-bright px-3 py-2">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium text-ink">{token.name}</p>
        <p className="text-xs text-ink-soft">
          Added {apiTokenDateLabel(token.created)} · Last used{' '}
          {apiTokenDateLabel(token.last_used)}
        </p>
      </div>
      <Button size="xs" variant="danger" disabled={busy} onClick={() => void onDelete(token)}>
        Delete
      </Button>
    </li>
  )
}

function ApiTokensSection() {
  const { data: tokens, error: loadError, reload } = useAsync(listApiTokens, [])
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [newToken, setNewToken] = useState<string | null>(null)

  const rows = tokens ?? []

  function openAddForm() {
    setName('')
    setError('')
    setAdding(true)
  }

  async function onAdd(event: SubmitEvent<HTMLFormElement>) {
    event.preventDefault()
    try {
      setBusy(true)
      setError('')
      const created = await createApiToken(name.trim() || 'API token')
      await reload()
      setAdding(false)
      setNewToken(created.token)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create the API token')
    } finally {
      setBusy(false)
    }
  }

  async function onDelete(token: ApiToken) {
    if (!window.confirm(`Remove "${token.name}"? Anything using it will stop working.`)) {
      return
    }
    try {
      setBusy(true)
      setError('')
      await deleteApiToken(token.id)
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove the API token')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className={sectionClassName}>
      <h2 className={sectionTitleClassName}>API tokens</h2>
      <p className={`${fieldHintClassName} mb-4`}>
        Authenticate uploads and other scripted access without a browser session. Create one token
        per script or device, so a compromised one can be revoked on its own.
      </p>

      {loadError && <p className="mb-3 text-sm text-madder">{loadError}</p>}
      {error && <p className="mb-3 text-sm text-madder">{error}</p>}

      {rows.length === 0 ? (
        <p className="mb-4 text-sm text-ink-soft">No API tokens yet.</p>
      ) : (
        <ul className="mb-4 flex flex-col gap-2">
          {rows.map((token) => (
            <ApiTokenRow key={token.id} token={token} busy={busy} onDelete={onDelete} />
          ))}
        </ul>
      )}

      {!adding && (
        <Button onClick={openAddForm} disabled={busy}>
          Create API token
        </Button>
      )}

      {adding && (
        <form className="flex flex-col gap-3" onSubmit={onAdd}>
          <label className={labelClassName}>
            <span className={labelTextClassName}>Name</span>
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              className={`${inputClassName} max-w-sm`}
              placeholder="e.g. upload script"
              autoFocus
            />
          </label>
          <p className={fieldHintClassName}>Something you will recognize, like where it runs.</p>
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={busy}>
              {busy ? 'Creating...' : 'Create token'}
            </Button>
            <Button variant="secondary" disabled={busy} onClick={() => setAdding(false)}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      <NewTokenDialog key={newToken} token={newToken} onClose={() => setNewToken(null)} />
    </section>
  )
}

export function AccountPage() {
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-5">
      <div>
        <h1 className="font-display text-2xl font-semibold tracking-tight text-ink">{t('account.title')}</h1>
        <p className="mt-1 text-sm text-ink-soft">{t('account.intro')}</p>
      </div>
      <SignedInSection />
      <AgentsSection />
      <PasskeysSection />
      <InstanceLimitsSection />
      <ApiTokensSection />
    </div>
  )
}
