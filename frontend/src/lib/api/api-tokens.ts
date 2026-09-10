import { apiFetch } from '../apiClient'

export type ApiToken = {
  id: string
  name: string
  created: string
  last_used: string
}

type ApiTokenListResponse = { api_tokens?: ApiToken[] }
type ApiTokenCreateResponse = { api_token: ApiToken; token: string }

export async function listApiTokens(): Promise<ApiToken[]> {
  const data = await apiFetch<ApiTokenListResponse>('/api/app/api-tokens', {
    fallbackError: 'Failed to load API tokens',
  })
  return data.api_tokens ?? []
}

/**
 * Mints a new token. The server hands back the raw value exactly once, in
 * `token` -- it is not recoverable afterwards, only its hash is stored.
 */
export function createApiToken(name: string) {
  return apiFetch<ApiTokenCreateResponse>('/api/app/api-tokens', {
    method: 'POST',
    body: { name },
    fallbackError: 'Failed to create the API token',
  })
}

export function deleteApiToken(id: string) {
  return apiFetch<unknown>(`/api/app/api-tokens/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    fallbackError: 'Failed to remove the API token',
  })
}

/**
 * Renders a PocketBase timestamp as a plain date, matching passkeyDateLabel.
 * Deliberately not toLocaleDateString: that would make e2e assertions depend
 * on the runner's locale.
 */
export function apiTokenDateLabel(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return 'Never'
  }
  return trimmed.slice(0, 10)
}
