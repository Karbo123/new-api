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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, Plus } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { PC_CHANNEL_META, PcChannelBadge, PcFuzzyScore, PcMoney, PcPeriod, PcPrice, PcVendorLogo, fmtLatency } from './pc-ui'
import { PcOfferTestButton, pcOfferTestKey, type PcOfferTestResult } from './offer-test'
import { pcEmptyStateText, type PriceCompareState } from '../use-price-compare'
import type { PcUnifiedRow } from '../types'

const PAGE_SIZE = 5
const TOP_N_OPTIONS = [
  { value: '1', label: 'Top 1' },
  { value: '3', label: 'Top 3' },
  { value: '5', label: 'Top 5' },
  { value: '10', label: 'Top 10' },
  { value: '0', label: 'Top ∞ (all)' },
]

export function SearchPanel({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [topN, setTopN] = useState('0')
  const [page, setPage] = useState(1)
  // ⚡测试按钮的实测延迟（key=渠道|ref|供应商|精度；null=测试失败），
  // 实测值以绿色覆盖「延迟」列
  const [testLatency, setTestLatency] = useState<Record<string, number | null>>(
    {}
  )
  // 实测不可调的报价（免费档批量实测标记）默认隐藏，勾选后显示
  const [showDead, setShowDead] = useState(false)
  // 计数只含当前渠道筛选下可能展示的 dead 行（与列表同一渠道口径）
  const deadCount = useMemo(
    () => pc.rows.filter((r) => r.dead && pc.channels[r.channel] !== false).length,
    [pc.rows, pc.channels]
  )
  const onTestResult = (key: string, r: PcOfferTestResult) =>
    setTestLatency((m) => ({
      ...m,
      [key]: r.ok && r.latency_ms != null ? r.latency_ms : null,
    }))
  // 表头排序：默认实际价格升序；null 值恒排最后
  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 }>({
    key: 'actual',
    dir: 1,
  })

  const toggleSort = (key: string) =>
    setSort((s) => (s.key === key ? { key, dir: (s.dir * -1) as 1 | -1 } : { key, dir: 1 }))

  // 搜索态跳过默认 actual 升序、列表实际按相关性排序时（见 filtered 内判断），
  // 排序箭头一并隐藏，避免指示器与真实排序不符
  const arrowHidden = query.trim() !== '' && sort.key === 'actual' && sort.dir === 1

  const { filtered, matched } = useMemo(() => {
    // 实测不可调（dead）默认不进列表，勾选「显示实测不可调」才出现；
    // 渠道开关语义同后端 chOk：键缺失 = 开启
    const arr = pc.rows
      .filter((r) => pc.channels[r.channel] !== false)
      .filter((r) => showDead || !r.dead)
    let out: PcUnifiedRow[]
    if (query.trim()) {
      const scored: { r: PcUnifiedRow; s: number }[] = arr.map((r) => ({
        r,
        s: PcFuzzyScore(
          r.name,
          r.full_name || r.name,
          r.vendor?.name || '',
          query
        ),
      }))
      const kept = scored.filter((x) => x.s > 0)
      kept.sort((a, b) => b.s - a.s || pc.actualOf(a.r) - pc.actualOf(b.r))
      out = kept.map((x) => x.r)
    } else {
      out = [...arr]
    }
    // 显式列排序（搜索态默认保持相关性序，其余按所选列）
    if (!(query.trim() && sort.key === 'actual' && sort.dir === 1)) {
      const val = (r: PcUnifiedRow): number | string | null => {
        switch (sort.key) {
          case 'name':
            return r.name
          case 'vendor':
            return r.vendor?.name ?? ''
          case 'channel':
            return r.channel
          case 'provider':
            return r.provider || ''
          case 'quant':
            return r.quant || ''
          case 'period':
            return r.period || ''
          case 'note':
            return r.note || ''
          case 'in':
            return r.in ?? null
          case 'out':
            return r.out ?? null
          case 'throughput':
            return r.throughput ?? null
          case 'latency':
            return r.latency ?? null
          default:
            return pc.actualOf(r)
        }
      }
      out.sort((a, b) => {
        const va = val(a)
        const vb = val(b)
        const aNull = va === null
        const bNull = vb === null
        if (aNull || bNull) return aNull && bNull ? 0 : aNull ? 1 : -1
        const c =
          typeof va === 'number' && typeof vb === 'number'
            ? va - vb
            : String(va).localeCompare(String(vb))
        return c * sort.dir
      })
    }
    const n = Number(topN)
    // matched = Top-N 截断前的匹配总数，底部文案报真实匹配数而非截断条数
    return { filtered: n > 0 ? out.slice(0, n) : out, matched: out.length }
  }, [pc.rows, pc.channels, pc.actualOf, query, topN, sort, showDead])

  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const safePage = Math.min(page, totalPages)
  const pageRows = filtered.slice(
    (safePage - 1) * PAGE_SIZE,
    safePage * PAGE_SIZE
  )

  // 底部公司清单：当前渠道筛选下的全部报价行按厂商显示名去重（含 Stealth 等
  // 匿名厂商）；不同前缀映射同一显示名（如 tencent/hunyuan→腾讯混元）只显示一次
  const vendors = useMemo(() => {
    const seen = new Map<string, { id: string; name: string; icon: string }>()
    pc.rows.forEach((r) => {
      if (pc.channels[r.channel] === false) return
      const v = r.vendor
      if (!v?.name) return
      const k = v.name.toLowerCase()
      if (!seen.has(k)) seen.set(k, { id: v.id || '', name: v.name, icon: v.icon || '' })
    })
    return [...seen.values()].sort((a, b) => a.name.localeCompare(b.name))
  }, [pc.rows, pc.channels])

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Unified search')}</CardTitle>
        <CardDescription>
          {t('Fuzzy search across channels & providers · offers fully expanded · Top-N & pagination')}
        </CardDescription>
        <div className='flex flex-wrap items-center gap-2 pt-1'>
          <Input
            className='w-full max-w-sm'
            placeholder={t('Search models, e.g. glm 5.3 / kimi code / deepseek flash') as string}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setPage(1)
            }}
          />
          <span className='text-muted-foreground text-xs'>
            {t('Top {{n}}', { n: Number(topN) > 0 ? topN : '∞' })}
          </span>
          <Select
            value={topN}
            onValueChange={(v) => {
              if (v) {
                setTopN(v)
                setPage(1)
              }
            }}
          >
            <SelectTrigger className='w-[130px]'>
              <SelectValue>
                {t(
                  TOP_N_OPTIONS.find((o) => o.value === topN)?.label ?? 'Top ∞ (all)'
                )}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {TOP_N_OPTIONS.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {t(o.label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className='text-muted-foreground ml-1 text-xs'>{t('Channels')}</span>
          {Object.keys(PC_CHANNEL_META).map((ch) => (
            <label
              key={ch}
              className='text-muted-foreground flex items-center gap-1 text-xs'
            >
              <Checkbox
                checked={pc.channels[ch] !== false}
                onCheckedChange={(v) => {
                  pc.changeChannels({ [ch]: v === true })
                  setPage(1)
                }}
              />
              {t(PC_CHANNEL_META[ch].label)}
            </label>
          ))}
          {deadCount > 0 && (
            <label className='text-muted-foreground flex items-center gap-1 text-xs'>
              <Checkbox
                checked={showDead}
                onCheckedChange={(v) => {
                  setShowDead(v === true)
                  setPage(1)
                }}
              />
              {t('Show tested-unreachable offers')} ({deadCount})
            </label>
          )}
          <span className='text-muted-foreground ml-auto text-xs'>
            {filtered.length} {t('offers')}
          </span>
        </div>
      </CardHeader>
      <CardContent>
        <div className='overflow-x-auto'>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className='w-10'>#</TableHead>
                {(
                  [
                    ['name', 'Model'],
                    ['vendor', 'Company'],
                    ['channel', 'Channel'],
                    ['provider', 'Provider'],
                    ['quant', 'Precision'],
                    ['period', 'Time period'],
                    ['note', 'Notes'],
                    ['in', 'Input'],
                    ['out', 'Output'],
                    ['actual', 'Actual ¥/M'],
                    ['throughput', 'Throughput tok/s'],
                    ['latency', 'Latency'],
                  ] as const
                ).map(([key, label]) => {
                  const right = ['in', 'out', 'actual', 'throughput', 'latency'].includes(key)
                  return (
                    <TableHead
                      key={key}
                      className={
                        'cursor-pointer select-none whitespace-nowrap hover:text-foreground' +
                        (right ? ' text-right' : '')
                      }
                      onClick={() => toggleSort(key)}
                      title={t('Click to sort') as string}
                    >
                      <span className='inline-flex items-center gap-0.5'>
                        {t(label)}
                        {sort.key === key &&
                          !arrowHidden &&
                          (sort.dir === 1 ? (
                            <ArrowUp className='h-3 w-3' />
                          ) : (
                            <ArrowDown className='h-3 w-3' />
                          ))}
                      </span>
                    </TableHead>
                  )
                })}
                <TableHead>{t('Actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pageRows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={14} className='text-muted-foreground text-center'>
                    {pc.unified.data == null || pc.unified.data.rows.length === 0
                      ? pcEmptyStateText(pc.unified.data, t, pc.unified.error)
                      : t('No matching models')}
                  </TableCell>
                </TableRow>
              ) : (
                pageRows.map((r, i) => (
                  <TableRow
                    key={`${r.channel}-${r.ref}-${r.provider}-${r.quant}-${r.period}-${i}`}
                    className={r.dead ? 'opacity-60 hover:opacity-100' : undefined}
                  >
                    <TableCell>{(safePage - 1) * PAGE_SIZE + i + 1}</TableCell>
                    <TableCell>
                      <span className='inline-flex items-center gap-1'>
                        <PcVendorLogo row={r} />
                        <b>{r.name}</b>
                      </span>
                    </TableCell>
                    <TableCell>{r.vendor?.name}</TableCell>
                    <TableCell>
                      <PcChannelBadge channel={r.channel} />
                    </TableCell>
                    <TableCell className='text-xs'>{r.provider || '—'}</TableCell>
                    <TableCell className='text-xs'>
                      {r.quant && !['—', 'unknown'].includes(r.quant.toLowerCase())
                        ? r.quant
                        : '—'}
                    </TableCell>
                    <TableCell>
                      <PcPeriod row={r} />
                    </TableCell>
                    <TableCell className='text-muted-foreground max-w-[180px] truncate text-xs' title={r.note || undefined}>
                      {r.note || '—'}
                    </TableCell>
                    <TableCell className='text-right'>
                      <PcPrice row={r} value={r.in} />
                    </TableCell>
                    <TableCell className='text-right'>
                      <PcPrice row={r} value={r.out} />
                    </TableCell>
                    <TableCell className='text-right'>
                      <PcMoney value={pc.actualOf(r)} />
                    </TableCell>
                    <TableCell className='text-right'>
                      {r.throughput != null
                        ? Number(r.throughput).toLocaleString()
                        : '—'}
                    </TableCell>
                    <TableCell className='text-right'>
                      {(() => {
                        const tested =
                          testLatency[
                            pcOfferTestKey(
                              r.channel,
                              r.ref,
                              r.provider,
                              r.quant
                            )
                          ]
                        if (tested != null) {
                          return (
                            <span className='tabular-nums text-emerald-600 dark:text-emerald-400'>
                              {fmtLatency(tested)}
                            </span>
                          )
                        }
                        return r.latency != null
                          ? fmtLatency(Number(r.latency))
                          : '—'
                      })()}
                    </TableCell>
                    <TableCell>
                      <div className='flex items-center gap-1'>
                        <PcOfferTestButton
                          channel={r.channel}
                          ref={r.ref}
                          provider={r.provider}
                          quant={r.quant}
                          onResult={onTestResult}
                        />
                        <Button
                          size='sm'
                          variant='outline'
                          className='h-7 px-2'
                          onClick={() => void pc.addToPriority(r)}
                          title={t('Add to priority list') as string}
                        >
                          <Plus className='h-3.5 w-3.5' />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
        <div className='text-muted-foreground mt-2 flex items-center justify-between text-xs'>
          <span>
            {query.trim()
              ? t('Fuzzy matched {{count}} offers, sorted by relevance then price', { count: matched })
              : t('All offers sorted by current actual price')}
            {Number(topN) > 0
              ? ` · ${t('showing top {{n}} only', { n: topN })}`
              : ''}
          </span>
          {filtered.length > 0 && (
            <div className='flex items-center gap-1'>
              <Button
                size='sm'
                variant='ghost'
                className='h-7'
                disabled={safePage <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
              >
                <ChevronLeft className='h-4 w-4' />
              </Button>
              <span>
                {safePage} / {totalPages}
              </span>
              <Button
                size='sm'
                variant='ghost'
                className='h-7'
                disabled={safePage >= totalPages}
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              >
                <ChevronRight className='h-4 w-4' />
              </Button>
            </div>
          )}
        </div>
        {vendors.length > 0 && (
          <div className='text-muted-foreground mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs'>
            <span className='font-medium'>{t('Supported companies')}</span>
            {vendors.map((v) => (
              <span
                key={v.name.toLowerCase()}
                className='inline-flex items-center gap-1'
                title={v.name}
              >
                <PcVendorLogo row={{ vendor: v } as PcUnifiedRow} size={14} />
                {v.name}
              </span>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
