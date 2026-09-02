/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'

import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import type { PcUnifiedRow } from '../types'

export const PC_CHANNEL_META: Record<
  string,
  { label: string; badgeClass: string; dot: string }
> = {
  openrouter: {
    label: 'OpenRouter',
    badgeClass: 'bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300',
    dot: '#3b82f6',
  },
  opencode: {
    label: 'OpenCode Go',
    badgeClass:
      'bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-300',
    dot: '#f59e0b',
  },
  deepseek: {
    label: 'DeepSeek official',
    badgeClass:
      'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300',
    dot: '#10b981',
  },
  apib: {
    label: 'APIB',
    badgeClass:
      'bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300',
    dot: '#8b5cf6',
  },
  buzzai: {
    label: 'BuzzAI',
    badgeClass:
      'bg-cyan-100 text-cyan-700 dark:bg-cyan-950 dark:text-cyan-300',
    dot: '#06b6d4',
  },
  apikl: {
    label: 'APIKL',
    badgeClass:
      'bg-rose-100 text-rose-700 dark:bg-rose-950 dark:text-rose-300',
    dot: '#f43f5e',
  },
  ikun: {
    label: 'IKUN',
    badgeClass:
      'bg-lime-100 text-lime-700 dark:bg-lime-950 dark:text-lime-300',
    dot: '#84cc16',
  },
}

export function PcChannelBadge({ channel }: { channel: string }) {
  const { t } = useTranslation()
  // 未知渠道兜底：中性灰徽章显示原始渠道名，不伪装成 OpenRouter
  const meta = PC_CHANNEL_META[channel]
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-md px-1.5 py-0.5 text-xs font-medium whitespace-nowrap',
        meta ? meta.badgeClass : 'bg-muted text-muted-foreground'
      )}
    >
      {meta ? t(meta.label) : channel}
    </span>
  )
}

export function PcVendorLogo({
  row,
  size = 16,
}: {
  row: PcUnifiedRow
  size?: number
}) {
  const icon = row.vendor?.icon
  const node = icon ? getLobeIcon(icon, size) : null
  if (node) {
    return <span className='mr-1 inline-flex align-middle'>{node}</span>
  }
  return (
    <span
      className='bg-muted text-muted-foreground mr-1 inline-flex items-center justify-center rounded text-[10px] font-semibold align-middle'
      style={{ width: size, height: size }}
    >
      {(row.vendor?.name || '?').slice(0, 1)}
    </span>
  )
}

export function PcPrice({ row, value }: { row: PcUnifiedRow; value: number }) {
  if (row.currency === 'CNY') {
    return <span>¥{value.toFixed(2)}</span>
  }
  return <span>${value < 0.1 ? value.toFixed(4) : value.toFixed(2)}</span>
}

export function PcMoney({ value }: { value: number }) {
  const digits = value != null && Math.abs(value) < 0.01 ? 5 : 3
  return (
    <span>
      ¥
      {Number(value).toLocaleString('zh-CN', {
        minimumFractionDigits: 3,
        maximumFractionDigits: digits,
      })}
    </span>
  )
}

// 高峰时段（北京时间工作日 9-12 / 14-18）：'peak' | 'off'
// 固定按 +8 时区取小时，与后端 pcIsPeak 一致，不随机器本地时区漂移
export function pcPeriodNowKey(): 'peak' | 'off' {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: 'Asia/Shanghai',
    weekday: 'short',
    hour: 'numeric',
    hour12: false,
  }).formatToParts(new Date())
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? ''
  const weekend = ['Sat', 'Sun'].includes(get('weekday'))
  const h = Number(get('hour')) % 24
  const inWindow = !weekend && ((h >= 9 && h < 12) || (h >= 14 && h < 18))
  return inWindow ? 'peak' : 'off'
}

export const PC_PERIOD_LABEL: Record<string, string> = {
  peak: '🔺 Peak',
  off: '🟢 Off-peak',
}

export function PcPeriod({ row }: { row: PcUnifiedRow }) {
  const { t } = useTranslation()
  if (!row.period) return <span className='text-muted-foreground'>—</span>
  const label = t(PC_PERIOD_LABEL[row.period] ?? row.period)
  if (row.period === pcPeriodNowKey()) {
    return (
      <span>
        <b>{label}</b> <span className='text-muted-foreground text-xs'>{t('now')}</span>
      </span>
    )
  }
  return <span>{label}</span>
}

export function fmtLatency(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`
}

export function PcFuzzyScore(rowName: string, rowFull: string, vendor: string, q: string): number {
  const toks = q
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
  if (toks.length === 0) return 0
  const norm = (s: string) => (s || '').toLowerCase().replace(/[^a-z0-9\u4e00-\u9fff]+/g, '')
  const name = norm(rowName)
  const full = norm(rowFull)
  const vend = norm(vendor)
  let sum = 0
  for (const tok of toks) {
    const nt = norm(tok)
    if (!nt) continue
    let best = 0
    if (name.startsWith(nt)) {
      best = 1.0
    } else if (name.includes(nt)) {
      best = 0.92
    } else if (full.includes(nt)) {
      best = 0.85
    } else if (vend && (vend.includes(nt) || (nt.includes(vend) && vend.length > 1))) {
      best = 0.8
    } else if (nt.length >= 3 && isSubseq(nt, full)) {
      best = 0.55
    }
    if (!best) return 0
    sum += best
  }
  return sum / toks.length
}

function isSubseq(needle: string, hay: string): boolean {
  if (!needle) return false
  let i = 0
  for (const ch of hay) {
    if (ch === needle[i]) i++
    if (i >= needle.length) return true
  }
  return false
}
