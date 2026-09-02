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
import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, Trash2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

import { PC_PERIOD_LABEL, PcChannelBadge, PcMoney, PcPeriod } from './pc-ui'
import { PcOfferTestButton } from './offer-test'
import type { PriceCompareState } from '../use-price-compare'
import type { PcPriorityEntry, PcUnifiedRow } from '../types'

export function PriorityPanel({ pc }: { pc: PriceCompareState }) {
  const { t } = useTranslation()
  // 默认折叠：只显示模型摘要行，点击展开才看该模型的具体渠道报价
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const toggle = (key: string) =>
    setExpanded((e) => ({ ...e, [key]: !e[key] }))

  const entries = useMemo(() => {
    const es = Object.entries(pc.priorityEntries)
    return es
      .map(([key, entry]: [string, PcPriorityEntry]) => {
        // 该模型当前各渠道报价（用于展示实时实际价；供应商维度参与匹配）
        const offers: { offer: (typeof entry.offers)[number]; row: PcUnifiedRow | undefined }[] =
          entry.offers.map((offer) => ({
            offer,
            row: pc.rows.find(
              (r) =>
                r.channel === offer.channel &&
                r.ref === offer.ref &&
                (r.period || '') === (offer.period || '') &&
                (r.provider || '') === (offer.provider || '')
            ),
          }))
        return { key, entry, offers }
      })
      .sort((a, b) => a.entry.name.localeCompare(b.entry.name))
  }, [pc.priorityEntries, pc.rows])

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t('Model priority list')}{' '}
          <Badge variant='secondary'>{entries.length}</Badge>
        </CardTitle>
        <CardDescription>
          {t('Add offers with ＋ in the unified search — top is highest priority')}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {entries.length === 0 && !pc.priorityPending && (
          <div className='text-muted-foreground py-8 text-center text-sm'>
            {t('Priority list is empty — click ＋ in the unified search to add channel offers of a model')}
          </div>
        )}
        {entries.map(({ key, entry, offers }) => (
          <div key={key} className='rounded-lg border p-3'>
            <button
              type='button'
              className='flex w-full cursor-pointer items-center gap-2 rounded-md p-1 text-left focus:outline-none'
              onClick={() => toggle(key)}
              title={t('Click to expand/collapse channel offers') as string}
            >
              {expanded[key] ? (
                <ChevronDown className='h-4 w-4 flex-none' />
              ) : (
                <ChevronRight className='h-4 w-4 flex-none' />
              )}
              <b>{entry.name}</b>
              <Badge variant='secondary' className='ml-auto flex-none'>
                {entry.offers.length}
              </Badge>
            </button>
            {expanded[key] && (
            <>
            <div className='mt-2 overflow-x-auto'>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className='w-10'>#</TableHead>
                    <TableHead>{t('Channel')}</TableHead>
                    <TableHead>{t('Offer')}</TableHead>
                    <TableHead>{t('Time period')}</TableHead>
                    <TableHead className='text-right'>
                      {t('Actual ¥/M (per 1M input)')}
                    </TableHead>
                    <TableHead className='w-28'>{t('Actions')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {offers.map(({ offer, row }, i) => (
                    <TableRow key={`${offer.channel}-${offer.ref}-${offer.provider}-${offer.period}-${i}`}>
                      <TableCell>{i + 1}</TableCell>
                      <TableCell>
                        <PcChannelBadge channel={offer.channel} />
                      </TableCell>
                      <TableCell>
                        {offer.provider}
                        <span className='text-muted-foreground ml-1 font-mono text-xs'>
                          {offer.ref}
                        </span>
                      </TableCell>
                      <TableCell>
                        {row ? (
                          <PcPeriod row={row} />
                        ) : (
                          <span className='text-muted-foreground'>
                            {offer.period
                              ? t(PC_PERIOD_LABEL[offer.period] ?? offer.period)
                              : '—'}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className='text-right'>
                        {row ? (
                          <PcMoney value={pc.actualOf(row)} />
                        ) : (
                          <span className='text-muted-foreground'>—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className='flex items-center gap-1'>
                          <PcOfferTestButton
                            channel={offer.channel}
                            ref={offer.ref}
                            provider={offer.provider}
                            quant={offer.quant}
                          />
                          <Button
                            size='sm'
                            variant='ghost'
                            className='h-6 px-1'
                            disabled={i === 0}
                            onClick={() => void pc.movePriority(key, i, -1)}
                          >
                            <ArrowUp className='h-3.5 w-3.5' />
                          </Button>
                          <Button
                            size='sm'
                            variant='ghost'
                            className='h-6 px-1'
                            disabled={i === offers.length - 1}
                            onClick={() => void pc.movePriority(key, i, 1)}
                          >
                            <ArrowDown className='h-3.5 w-3.5' />
                          </Button>
                          <Button
                            size='sm'
                            variant='ghost'
                            className='text-destructive h-6 px-1'
                            onClick={() => void pc.removeFromPriority(key, i)}
                          >
                            <Trash2 className='h-3.5 w-3.5' />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
            <div className='text-muted-foreground mt-1 text-xs'>
              {t('Row matched by channel + model + provider + period after data refreshes')}
            </div>
            </>
            )}
          </div>
        ))}
        {entries.length > 0 && (
          <div className='text-muted-foreground text-xs'>
            {t('— means the channel no longer lists that offer; remove it or refresh')}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
