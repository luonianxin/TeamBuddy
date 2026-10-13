export interface TruncMarker {
  bytes: number
  omitted: number
  file: boolean
  redacted: boolean
  capped: boolean
}

const markerRe =
  /\r?\n⟦gt-truncated v1 bytes=(\d+) omitted=(\d+) file=([01])( redacted=1)?( capped=1)?⟧\r?$/

export function parseTruncMarker(content: string): { text: string; marker: TruncMarker | null } {
  const match = markerRe.exec(content || '')
  if (!match) return { text: content || '', marker: null }
  return {
    text: content.slice(0, match.index),
    marker: {
      bytes: Number(match[1]),
      omitted: Number(match[2]),
      file: match[3] === '1',
      redacted: Boolean(match[4]),
      capped: Boolean(match[5]),
    },
  }
}
