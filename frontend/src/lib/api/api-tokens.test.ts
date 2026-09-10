import { describe, expect, it } from 'vitest'
import { apiTokenDateLabel } from './api-tokens'

describe('apiTokenDateLabel', () => {
  it('shortens a PocketBase timestamp to the date', () => {
    expect(apiTokenDateLabel('2026-08-28 05:56:12.123Z')).toBe('2026-08-28')
  })

  it('reports a token that has never been used', () => {
    expect(apiTokenDateLabel('')).toBe('Never')
    expect(apiTokenDateLabel('   ')).toBe('Never')
  })

  it('leaves an already-short value alone', () => {
    expect(apiTokenDateLabel('2026-08-28')).toBe('2026-08-28')
  })
})
