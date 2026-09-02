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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Plus, Trash2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import { api } from '@/lib/api'
import { toast } from 'sonner'

import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { getPcMapping } from '../api'
import type { PcBenchMapRow, PcMappingResponse, PcMappingRow } from '../types'
import { PcChannelBadge } from './pc-ui'

const SOURCE_LABEL: Record<string, string> = {
  or: 'Auto',
  family: 'Family version',
  exact: 'Manual alias',
  regex: 'Regex rule',
  builtin: 'Built-in',
  snapshot: 'Snapshot',
}

// 与后端 pcCanonKey 一致：小写、去 :free 后缀、只留字母数字
const canonKey = (s: string) =>
  s
    .toLowerCase()
    .trim()
    .replace(/:free$/, '')
    .replace(/[^a-z0-9]/g, '')

// 基准源（与后端 bench pins 的 key 前缀一致）
const BENCH_SOURCES = ['livebench', 'arena', 'aa', 'da', 'oc', 'ls', 'aiq', 'orr'] as const

export function AliasCard() {
  const { t } = useTranslation()
  const [mapping, setMapping] = useState<PcMappingResponse | null>(null)
  // mapping 为 null 时区分「加载中」与「加载失败」，否则表格空白两者不可辨
  const [mapError, setMapError] = useState(false)
  const [unmatchedOnly, setUnmatchedOnly] = useState(false)
  const [scope, setScope] = useState<'channel' | 'bench'>('channel')
  const [exact, setExact] = useState<Record<string, string>>({})
  const [bench, setBench] = useState<Record<string, string>>({})
  const [bnSrc, setBnSrc] = useState<string>('livebench')
  const [bnName, setBnName] = useState('')
  const [bnDst, setBnDst] = useState('')
  const [catalog, setCatalog] = useState<string[]>([])
  const [editing, setEditing] = useState<PcMappingRow | null>(null)
  const [input, setInput] = useState('')
  const [saving, setSaving] = useState(false)
  const [listOpen, setListOpen] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const bnDstRef = useRef<HTMLInputElement>(null)

  const loadAll = useCallback(() => {
    setMapError(false)
    getPcMapping()
      .then(setMapping)
      .catch(() => setMapError(true))
    api
      .get('/api/price_compare/aliases')
      .then((res) => {
        if (res.data.success) {
          setExact(res.data.data.exact ?? {})
          setBench(res.data.data.bench ?? {})
          setCatalog(res.data.data.catalog ?? [])
        }
      })
      .catch(() => undefined)
  }, [])

  useEffect(() => {
    loadAll()
  }, [loadAll])

  // 保存基准名归属（后端对未携带段保留原值，渠道修正不受影响）
  const saveBench = async (next: Record<string, string>) => {
    setSaving(true)
    try {
      const res = await api.post('/api/price_compare/aliases', { bench: next })
      // 后端写文件失败等仍返回 HTTP 200 + success:false，需检查避免误报成功
      if (!res.data.success) throw new Error(String(res.data.message || 'error'))
      setBench(next)
      toast.success(t('Mapping saved, merging rebuilt'))
      loadAll()
    } catch {
      toast.error(t('Failed to save alias rules'))
    } finally {
      setSaving(false)
    }
  }

  const addBench = () => {
    const name = bnName.trim()
    const dst = bnDst.trim()
    if (!name || !dst) return
    const next = { ...bench, [`${bnSrc}|${name}`]: dst }
    setBnName('')
    setBnDst('')
    void saveBench(next)
  }

  const openFix = (r: PcMappingRow) => {
    setEditing(r)
    setInput(r.target || '')
    setTimeout(() => inputRef.current?.focus(), 60)
  }

  // 手工映射的 exact 键可能是 ref 或 name 的 canon，恢复时两个候选都清掉
  const manualKeyOf = (r: PcMappingRow) => {
    for (const k of [canonKey(r.ref), canonKey(r.name)]) {
      if (k && exact[k] !== undefined) return k
    }
    return null
  }

  const saveMapping = async () => {
    if (!editing) return
    setSaving(true)
    const key = canonKey(editing.ref) || canonKey(editing.name)
    const next = { ...exact }
    if (input.trim()) next[key] = input.trim()
    else delete next[key]
    try {
      const res = await api.post('/api/price_compare/aliases', { exact: next })
      if (!res.data.success) throw new Error(String(res.data.message || 'error'))
      setExact(next)
      setEditing(null)
      toast.success(t('Mapping saved, merging rebuilt'))
      loadAll()
    } catch {
      toast.error(t('Failed to save alias rules'))
    } finally {
      setSaving(false)
    }
  }

  const restoreAuto = async () => {
    if (!editing || saving) return
    const next = { ...exact }
    for (const k of [canonKey(editing.ref), canonKey(editing.name)]) {
      if (k) delete next[k]
    }
    try {
      setSaving(true)
      const res = await api.post('/api/price_compare/aliases', { exact: next })
      if (!res.data.success) throw new Error(String(res.data.message || 'error'))
      setExact(next)
      setEditing(null)
      toast.success(t('Mapping saved, merging rebuilt'))
      loadAll()
    } catch {
      toast.error(t('Failed to save alias rules'))
    } finally {
      setSaving(false)
    }
  }

  const suggestions = useMemo(() => {
    const q = input.trim().toLowerCase()
    if (!q) return []
    // 输入与目录名完全一致时不再弹建议（避免面板挡住保存按钮）
    return catalog
      .filter((n) => n.toLowerCase().includes(q) && n.toLowerCase() !== q)
      .slice(0, 12)
  }, [input, catalog])

  const rows = (mapping?.rows ?? []).filter(
    (r) => !unmatchedOnly || !r.matched
  )
  const benchRowsAll = mapping?.bench?.rows ?? []
  const benchRows = benchRowsAll.filter((r) => !unmatchedOnly || !r.matched)
  const activeRows: (PcMappingRow | PcBenchMapRow)[] =
    scope === 'channel' ? rows : benchRows
  const [mpPage, setMpPage] = useState(1)
  const MP_SIZE = 5
  const mpTotal = Math.max(1, Math.ceil(activeRows.length / MP_SIZE))
  const mpSafe = Math.min(mpPage, mpTotal)
  const mpRows = activeRows.slice((mpSafe - 1) * MP_SIZE, mpSafe * MP_SIZE)
  const activeUnmatched =
    scope === 'channel'
      ? (mapping?.unmatched ?? 0)
      : (mapping?.bench?.unmatched ?? 0)
  const activeTotal =
    scope === 'channel' ? (mapping?.total ?? 0) : (mapping?.bench?.total ?? 0)
  // 行数收缩（勾选 Unmatched only / 数据刷新）时同步收缩页码，
  // 避免 mpPage 远大于 mpTotal 时 Prev 连点多次无可见效果
  useEffect(() => {
    setMpPage((p) => Math.min(p, mpTotal))
  }, [mpTotal])
  // 表体空态文案：区分 加载中 / 加载失败 / 全部已匹配 / 暂无数据（旧库未抓取行情）
  const emptyText = (() => {
    if (!mapping) return mapError ? t('Failed to load') : t('Loading')
    if (unmatchedOnly && activeUnmatched === 0) {
      return t('All matched — no fixes needed')
    }
    return t('No data yet — click "Refresh now" to fetch')
  })()

  // Benchmark 行修正：预填钉住表单（来源+行名+当前目标），聚焦目标输入
  const openBenchFix = (r: PcBenchMapRow) => {
    setBnSrc(BENCH_SOURCES.includes(r.source as (typeof BENCH_SOURCES)[number]) ? r.source : 'livebench')
    setBnName(r.bench_name)
    setBnDst(r.matched ? r.target : '')
    setTimeout(() => bnDstRef.current?.focus(), 60)
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Model name mappings')}</CardTitle>
        <CardDescription>
          {t(
            'Each channel model / benchmark leaderboard row with the OpenRouter name it currently maps to — click Fix and fill in the right model name if wrong'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <Tabs
          value={scope}
          onValueChange={(v) => {
            setScope(v as 'channel' | 'bench')
            setMpPage(1)
          }}
        >
          <TabsList className='group-data-horizontal/tabs:h-auto'>
            <TabsTrigger value='channel' className='px-3 py-1 text-xs'>
              {t('Channel models')}
            </TabsTrigger>
            <TabsTrigger value='bench' className='px-3 py-1 text-xs'>
              {t('Benchmark rows')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className='flex flex-wrap items-center gap-2'>
          <Badge variant='secondary'>{activeTotal}</Badge>
          {activeUnmatched > 0 && (
            <Badge variant='destructive'>
              {activeUnmatched} {t('unmatched')}
            </Badge>
          )}
          <label className='text-muted-foreground ml-auto flex cursor-pointer items-center gap-1.5 text-xs'>
            <Checkbox
              checked={unmatchedOnly}
              onCheckedChange={(v) => setUnmatchedOnly(v === true)}
            />
            {t('Unmatched only')}
          </label>
        </div>
        {scope === 'channel' ? (
          <div className='rounded-md border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-24'>{t('Channel')}</TableHead>
                  <TableHead>{t('Channel model')}</TableHead>
                  <TableHead>{t('OpenRouter name')}</TableHead>
                  <TableHead className='w-24'>{t('Basis')}</TableHead>
                  <TableHead className='w-16' />
                </TableRow>
              </TableHeader>
              <TableBody>
                {mpRows.map((r0) => {
                  const r = r0 as PcMappingRow
                  return (
                    <TableRow
                      key={`${r.channel}-${r.ref}-${r.name}`}
                      className={r.matched ? '' : 'bg-destructive/5'}
                    >
                      <TableCell>
                        <PcChannelBadge channel={r.channel} />
                      </TableCell>
                      <TableCell className='font-mono text-xs'>{r.name}</TableCell>
                      <TableCell className='text-xs'>
                        {r.matched ? (
                          <b>{r.target}</b>
                        ) : (
                          <span className='text-destructive'>
                            {t('Unmatched (kept as-is)')}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className='text-muted-foreground text-xs'>
                        {r.matched ? t(SOURCE_LABEL[r.source] ?? r.source) : '—'}
                      </TableCell>
                      <TableCell>
                        <Button
                          size='sm'
                          variant='ghost'
                          className='h-6 px-1'
                          onClick={() => openFix(r)}
                        >
                          {t('Fix')}
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
                {mpRows.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={5}
                      className='text-muted-foreground text-center'
                    >
                      {emptyText}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        ) : (
          <div className='rounded-md border'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className='w-24'>{t('Source')}</TableHead>
                  <TableHead>{t('Benchmark row')}</TableHead>
                  <TableHead>{t('OpenRouter name')}</TableHead>
                  <TableHead className='w-28'>{t('Basis')}</TableHead>
                  <TableHead className='w-20' />
                </TableRow>
              </TableHeader>
              <TableBody>
                {mpRows.map((r0) => {
                  const b = r0 as PcBenchMapRow
                  return (
                    <TableRow
                      key={`${b.source}-${b.bench_name}`}
                      className={b.matched ? '' : 'bg-destructive/5'}
                    >
                      <TableCell className='text-xs'>{b.source}</TableCell>
                      <TableCell className='font-mono text-xs'>
                        {b.bench_name}
                      </TableCell>
                      <TableCell className='text-xs'>
                        {b.matched ? (
                          <b>{b.target}</b>
                        ) : (
                          <span className='text-destructive'>
                            {t('Unmatched (kept as-is)')}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className='text-muted-foreground text-xs'>
                        {b.matched
                          ? b.pinned
                            ? t('Pinned')
                            : `${t('Auto')} ${Math.round(b.sim * 100)}%`
                          : '—'}
                      </TableCell>
                      <TableCell>
                        {b.matched && (
                          <div className='flex items-center gap-0.5'>
                            <Button
                              size='sm'
                              variant='ghost'
                              className='h-6 px-1'
                              onClick={() => openBenchFix(b)}
                            >
                              {t('Fix')}
                            </Button>
                            {b.pinned && (
                              <Button
                                size='sm'
                                variant='outline'
                                className='h-6 px-1.5'
                                disabled={saving}
                                onClick={() => {
                                  const next = { ...bench }
                                  delete next[`${b.source}|${b.bench_name}`]
                                  void saveBench(next)
                                }}
                                title={t('Restore auto matching') as string}
                              >
                                <Trash2 className='h-3 w-3' />
                              </Button>
                            )}
                          </div>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
                {mpRows.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={5}
                      className='text-muted-foreground text-center'
                    >
                      {emptyText}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        )}
        <div className='flex items-center justify-end gap-2'>
          <span className='text-muted-foreground text-xs'>
            {mpSafe} / {mpTotal} {t('pages')} · {activeRows.length} {t('rows')}
          </span>
          <Button
            size='sm'
            variant='outline'
            className='h-7 px-2'
            disabled={mpSafe <= 1}
            onClick={() => setMpPage((p) => Math.max(1, p - 1))}
          >
            <ChevronLeft className='h-3.5 w-3.5' />
            {t('Previous page')}
          </Button>
          <Button
            size='sm'
            variant='outline'
            className='h-7 px-2'
            disabled={mpSafe >= mpTotal}
            onClick={() => setMpPage((p) => Math.min(mpTotal, p + 1))}
          >
            {t('Next page')}
            <ChevronRight className='h-3.5 w-3.5' />
          </Button>
        </div>

        {/* 钉住表单：修正 Benchmark 行时预填（来源+行名+当前目标），保存后覆盖自动匹配 */}
        {scope === 'bench' && (
          <div className='space-y-2 border-t pt-3'>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Pin a benchmark leaderboard row to the right OpenRouter model — the pinned row replaces auto matching for that model'
              )}
            </p>
            <div className='flex flex-wrap items-center gap-1.5'>
              <Select value={bnSrc} onValueChange={(v) => v && setBnSrc(v)}>
                <SelectTrigger className='w-32'>
                  <SelectValue>{bnSrc}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {BENCH_SOURCES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                className='w-56 text-xs'
                placeholder={t('Benchmark row name') as string}
                value={bnName}
                onChange={(e) => setBnName(e.target.value)}
              />
              <Input
                ref={bnDstRef}
                className='w-56 font-mono text-xs'
                placeholder={t('Belongs to OpenRouter model') as string}
                value={bnDst}
                onChange={(e) => setBnDst(e.target.value)}
              />
              <Button
                size='sm'
                disabled={saving || !bnName.trim() || !bnDst.trim()}
                onClick={() => void addBench()}
              >
                <Plus className='h-3.5 w-3.5' />
                {t('Add')}
              </Button>
            </div>
          </div>
        )}
      </CardContent>

      <Dialog
        open={editing != null}
        onOpenChange={(v) => {
          if (!v) setEditing(null)
        }}
      >
        <DialogContent className='sm:max-w-lg'>
          <DialogHeader>
            <DialogTitle>{t('Fix mapping')}</DialogTitle>
            <DialogDescription>
              {t(
                'Pick an existing OpenRouter model name from the list, or type a new one'
              )}
            </DialogDescription>
          </DialogHeader>
          {editing && (
            <div className='space-y-3'>
              <div className='text-sm'>
                <span className='text-muted-foreground'>
                  {t('Channel model')}:{' '}
                </span>
                <span className='font-mono text-xs'>{editing.name}</span>
              </div>
              <div className='text-sm'>
                <span className='text-muted-foreground'>
                  {t('Current mapping')}:{' '}
                </span>
                {editing.matched ? (
                  <b>{editing.target}</b>
                ) : (
                  <span className='text-destructive'>
                    {t('Unmatched (kept as-is)')}
                  </span>
                )}
              </div>
              <div className='relative'>
                <Input
                  ref={inputRef}
                  className='font-mono text-xs'
                  placeholder={t(
                    'Search or enter an OpenRouter model name'
                  ) as string}
                  value={input}
                  onChange={(e) => {
                    setInput(e.target.value)
                    setListOpen(true)
                  }}
                  onFocus={() => setListOpen(true)}
                  onBlur={() => setListOpen(false)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') void saveMapping()
                    if (e.key === 'Escape') setListOpen(false)
                  }}
                />
                {listOpen && suggestions.length > 0 && (
                  <div className='bg-popover absolute top-full z-50 mt-1 max-h-56 w-full overflow-auto rounded-md border shadow-md'>
                    {suggestions.map((n) => (
                      <button
                        key={n}
                        type='button'
                        className='hover:bg-muted w-full cursor-pointer px-3 py-1.5 text-left font-mono text-xs'
                        onMouseDown={(e) => {
                          e.preventDefault() // 防止 input 先失焦
                          setInput(n)
                          setListOpen(false)
                        }}
                      >
                        {n}
                      </button>
                    ))}
                  </div>
                )}
              </div>
              <div className='flex items-center gap-2'>
                {manualKeyOf(editing) && (
                  <Button
                    size='sm'
                    variant='ghost'
                    className='text-muted-foreground'
                    onClick={() => void restoreAuto()}
                  >
                    {t('Restore auto matching')}
                  </Button>
                )}
                <span className='flex-1' />
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() => setEditing(null)}
                >
                  {t('Cancel')}
                </Button>
                <Button size='sm' disabled={saving} onClick={() => void saveMapping()}>
                  {t('Save')}
                </Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </Card>
  )
}
