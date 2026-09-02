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
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { api } from '@/lib/api'
import { PC_SOURCES } from '../use-price-compare'
import type { PriceCompareState } from '../use-price-compare'
import type { PcArenaEntry, PcOrPriceDetail, PcUnifiedRow } from '../types'
import { PcChannelBadge, PcMoney, PcPeriod, PcPrice, PcVendorLogo } from './pc-ui'

export function ModelDialog({
  pc,
  openKey,
  onClose,
}: {
  pc: PriceCompareState
  openKey: string | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [orDetail, setOrDetail] = useState<PcOrPriceDetail | null>(null)
  const [orLoading, setOrLoading] = useState(false)
  const [orError, setOrError] = useState('')
  // 组件常驻不卸载：记录当前弹窗模型，请求返回时若已关闭或切到其它模型则丢弃过期响应
  const openKeyRef = useRef<string | null>(openKey)
  openKeyRef.current = openKey
  const expandProviders = async (ref: string) => {
    const key = openKey
    setOrLoading(true)
    setOrError('')
    try {
      const res = await api.get('/api/price_compare/providers', {
        params: { model: ref },
      })
      if (openKeyRef.current !== key) return
      if (res.data.success) setOrDetail(res.data.data as PcOrPriceDetail)
      else setOrError(String(res.data.message || 'error'))
    } catch (e) {
      if (openKeyRef.current !== key) return
      setOrError(String((e as Error).message || e))
    } finally {
      setOrLoading(false)
    }
  }
  const resetDetail = () => {
    setOrDetail(null)
    setOrError('')
    setOrLoading(false)
  }
  const offers: PcUnifiedRow[] = openKey
    ? pc.rows
        .filter((r) => r.key === openKey && pc.channels[r.channel] !== false)
        .sort((a, b) => pc.actualOf(a) - pc.actualOf(b))
    : []
  const first = offers[0]
  const arenaTip = (e: PcArenaEntry | null) =>
    e
      ? `Net ${e.net ?? '—'}% ±${e.net_ci ?? '—'} · ${t('Success')} ${e.success ?? '—'}% · ${t('Praise/complaint')} ${e.praise ?? '—'}% · ${t('Steerability')} ${e.steer ?? '—'}% · ${t('Sessions')} ${e.sessions ?? '—'} · ${t('Cost/task')} $${e.cost_per_task ?? '—'}`
      : ''

  return (
    <Dialog
      open={openKey != null}
      onOpenChange={(v) => {
        if (!v) {
          resetDetail()
          onClose()
        }
      }}
    >
      <DialogContent className='max-h-[85vh] sm:max-w-4xl overflow-auto'>
        <DialogHeader>
          <DialogTitle className='flex items-center gap-2'>
            {first && <PcVendorLogo row={first} size={20} />}
            {first?.name ?? ''}
            <span className='text-muted-foreground text-sm font-normal'>
              {t('{{count}} channel offers, sorted by actual price', {
                count: offers.length,
              })}
            </span>
          </DialogTitle>
          <DialogDescription>
            {t('Offers of the same model across channels')}
          </DialogDescription>
        </DialogHeader>
        {first?.lb && (
          <div className='flex flex-wrap items-center gap-1.5'>
            {(() => {
              const lbRec = first!.bench?.livebench?.scores ?? {}
              const lbDims =
                PC_SOURCES.find((s) => s.key === 'livebench')?.dims ?? []
              return lbDims.map((d) => (
                <Badge key={d.key} variant='secondary'>
                  {t(d.label)}{' '}
                  <b>
                    {lbRec[d.key.split(':')[1]] != null
                      ? Number(lbRec[d.key.split(':')[1]]).toFixed(1)
                      : '—'}
                  </b>
                </Badge>
              ))
            })()}
            <span className='text-muted-foreground text-xs'>
              {t('LiveBench match')}: {first.lb_name ?? '—'}
              {first.lb_sim != null ? ` (${(first.lb_sim * 100).toFixed(0)}%)` : ''}
            </span>
          </div>
        )}
        {first?.arena && (first.arena.code || first.arena.overall) && (
          <div className='flex flex-wrap items-center gap-1.5'>
            {(['code', 'overall'] as const).map((k) => {
              const e: PcArenaEntry | null = first.arena![k]
              if (!e) return null
              return (
                <Badge
                  key={k}
                  variant='outline'
                  title={arenaTip(e)}
                  className='cursor-help'
                >
                  {k === 'code' ? t('Arena Code') : t('Arena Overall')}{' '}
                  <b>
                    #{e.rank ?? '—'} {e.net ?? '—'}%
                  </b>
                </Badge>
              )
            })}
            <span className='text-muted-foreground text-xs'>
              arena.ai · {first.arena.name} (
              {(first.arena.similarity * 100).toFixed(0)}%) ·{' '}
              {t('Net Improvement, higher is better')}
            </span>
          </div>
        )}
        {(() => {
          // Elo 五榜徽标（text/webdev/vision/search/t2i，来自 row.bench.arena）
          const sc = first?.bench?.arena?.scores
          if (!sc) return null
          const dims = PC_SOURCES.find((s) => s.key === 'arena')?.dims ?? []
          const items = dims
            .filter((d) => d.key.endsWith('_elo') && sc[d.key.split(':')[1]] != null)
            .map((d) => {
              const field = d.key.split(':')[1]
              return {
                key: d.key,
                label: d.label,
                elo: sc[field],
                rank: sc[field.replace('_elo', '_rank')],
              }
            })
          if (items.length === 0) return null
          return (
            <div className='flex flex-wrap items-center gap-1.5'>
              {items.map((it) => (
                <Badge key={it.key} variant='outline'>
                  {t(it.label)}{' '}
                  <b>
                    {it.elo}
                    {it.rank ? ` (#${it.rank})` : ''}
                  </b>
                </Badge>
              ))}
              <span className='text-muted-foreground text-xs'>arena.ai</span>
            </div>
          )
        })()}
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('Channel')}</TableHead>
              <TableHead>{t('Time period')}</TableHead>
              <TableHead>{t('Provider')}</TableHead>
              <TableHead className='text-right'>{t('Input (list)')}</TableHead>
              <TableHead className='text-right'>{t('Output (list)')}</TableHead>
              <TableHead className='text-right'>{t('Cache read')}</TableHead>
              <TableHead className='text-right'>{t('Throughput tok/s')}</TableHead>
              <TableHead className='text-right'>{t('Actual ¥/M')}</TableHead>
              <TableHead>{t('Actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {offers.map((o, i) => (
              <TableRow key={i}>
                <TableCell>
                  <PcChannelBadge channel={o.channel} />
                </TableCell>
                <TableCell>
                  <PcPeriod row={o} />
                </TableCell>
                <TableCell>
                  {o.provider}
                  {o.quant && o.quant !== '—' && (
                    <span className='text-muted-foreground text-xs'> · {o.quant}</span>
                  )}
                  {o.note && (
                    <span className='text-muted-foreground text-xs'> · {o.note}</span>
                  )}
                </TableCell>
                <TableCell className='text-right'>
                  <PcPrice row={o} value={o.in} />
                </TableCell>
                <TableCell className='text-right'>
                  <PcPrice row={o} value={o.out} />
                </TableCell>
                <TableCell className='text-right'>
                  <PcPrice row={o} value={o.read} />
                </TableCell>
                <TableCell className='text-right'>
                  {o.throughput != null
                    ? Number(o.throughput).toLocaleString()
                    : '—'}
                </TableCell>
                <TableCell className='text-right'>
                  <PcMoney value={pc.actualOf(o)} />
                </TableCell>
                <TableCell>
                  <Button
                    size='sm'
                    variant='outline'
                    className='h-7'
                    onClick={() => void pc.addToPriority(o)}
                  >
                    {t('Add to priority list')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {offers.some((o) => o.channel === 'openrouter') && (
          <div className='pt-1'>
            <Button
              size='sm'
              variant='outline'
              disabled={orLoading}
              onClick={() => expandProviders(offers.find((o) => o.channel === 'openrouter')!.ref)}
            >
              {orLoading ? t('Fetching providers…') : t('Expand all OpenRouter provider offers')}
            </Button>
            {orError && <div className='text-destructive mt-1 text-xs'>{orError}</div>}
            {orDetail && (
              <div className='mt-2'>
                <div className='text-muted-foreground mb-1 text-xs'>
                  {t('Fetched at')}: {new Date(orDetail.fetched_at * 1000).toLocaleString()} ·{' '}
                  {orDetail.providers.length} {t('providers')}
                </div>
                <div className='max-h-72 overflow-auto rounded-md border'>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>#</TableHead>
                        <TableHead>{t('Provider')}</TableHead>
                        <TableHead>{t('Quantization')}</TableHead>
                        <TableHead className='text-right'>{t('Input (list)')}</TableHead>
                        <TableHead className='text-right'>{t('Output (list)')}</TableHead>
                        <TableHead className='text-right'>{t('Throughput tok/s')}</TableHead>
                        <TableHead className='text-right'>{t('Actual ¥/M')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(() => {
                        const R = pc.effRatio()
                        return [...orDetail.providers]
                          .map((p) => ({
                            p,
                            actual:
                              (R.uncached * p.prompt * 1e6 +
                                R.cache * p.cache_read * 1e6 +
                                (R.cache_write || 0) * p.prompt * 1e6 +
                                R.output * p.completion * 1e6) *
                              pc.rate,
                          }))
                          .sort((a, b) => a.actual - b.actual)
                          .map(({ p, actual }, i) => (
                            <TableRow key={i}>
                              <TableCell>{i + 1}</TableCell>
                              <TableCell>
                                {p.provider}
                                <span className='text-muted-foreground ml-1 text-xs'>
                                  {p.quantization}
                                </span>
                              </TableCell>
                              <TableCell className='text-right'>
                                ${(p.prompt * 1e6).toFixed(4)}
                              </TableCell>
                              <TableCell className='text-right'>
                                ${(p.completion * 1e6).toFixed(4)}
                              </TableCell>
                              <TableCell className='text-right'>
                                {p.throughput_p50 != null
                                  ? Number(p.throughput_p50).toLocaleString()
                                  : '—'}
                              </TableCell>
                              <TableCell className='text-right'>
                                <PcMoney value={actual} />
                              </TableCell>
                            </TableRow>
                          ))
                      })()}
                    </TableBody>
                  </Table>
                </div>
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
