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
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Loader2, SkipForward, X } from 'lucide-react'

import { cn } from '@/lib/utils'

import type { PcRefreshStageState, PcRefreshStatus } from '../types'
import { PC_REFRESH_STAGES } from '../use-price-compare'

// 阶段节点状态样式：pending 空心序号 → running 主色旋转 → done 主色实心勾 →
// skipped 虚线灰 → failed 红叉。颜色全部走主题 token（bg-primary/text-muted-
// foreground/text-destructive），自动适配明暗主题
function stageIcon(status: PcRefreshStageState['status'], index: number) {
  switch (status) {
    case 'running':
      return (
        <>
          <Loader2 className='h-3.5 w-3.5 animate-spin' />
          <span className='absolute inset-0 animate-ping rounded-full bg-primary/20' />
        </>
      )
    case 'done':
      return <Check className='h-3.5 w-3.5' />
    case 'skipped':
      return <SkipForward className='h-3 w-3' />
    case 'failed':
      return <X className='h-3.5 w-3.5' />
    default:
      return <span className='text-[11px] font-medium tabular-nums'>{index + 1}</span>
  }
}

function stageCircleCls(status: PcRefreshStageState['status']): string {
  switch (status) {
    case 'running':
      return 'border-primary bg-primary/10 text-primary'
    case 'done':
      return 'border-primary bg-primary text-primary-foreground'
    case 'skipped':
      return 'border-dashed border-muted-foreground/40 bg-transparent text-muted-foreground/50'
    case 'failed':
      return 'border-destructive bg-destructive text-destructive-foreground'
    default:
      return 'border-border bg-background text-muted-foreground'
  }
}

function StageNode({ stage, index }: { stage: PcRefreshStageState; index: number }) {
  const { t } = useTranslation()
  const def = PC_REFRESH_STAGES.find((d) => d.key === stage.key)
  const label = def ? t(def.label) : stage.label
  const hasCounts =
    (stage.status === 'running' || stage.status === 'done') &&
    (stage.total ?? 0) > 0
  return (
    // 弹性宽度：随容器收窄（窄弹窗不裁切），宽容器由连接线吃掉剩余空间
    <div className='flex min-w-0 flex-1 flex-col items-center gap-1'>
      <div
        className={cn(
          'relative flex h-7 w-7 items-center justify-center rounded-full border-2 transition-colors',
          stageCircleCls(stage.status)
        )}
      >
        {stageIcon(stage.status, index)}
      </div>
      <span
        className='text-foreground line-clamp-2 text-center text-xs leading-tight font-medium'
        title={label}
      >
        {label}
      </span>
      {/* 阶段内并行子进度（如 17 项数据源并行抓取）+ 当前子项名 */}
      {hasCounts && (
        <span className='text-muted-foreground tabular-nums'>
          {stage.done}/{stage.total}
        </span>
      )}
      {stage.status === 'running' && stage.current && (
        <span
          className='text-muted-foreground max-w-full truncate text-[11px]'
          title={stage.current}
        >
          {stage.current}
        </span>
      )}
      {stage.status === 'skipped' && (
        <span className='text-muted-foreground/70 text-[11px]'>
          {t('Skipped')}
        </span>
      )}
      {stage.status === 'failed' && (
        <span
          className='text-destructive max-w-full truncate text-[11px]'
          title={stage.error}
        >
          {t('Failed')}
        </span>
      )}
      {stage.status === 'pending' && (
        <span className='text-muted-foreground/60 text-[11px]'>
          {t('Not started')}
        </span>
      )}
    </div>
  )
}

// 左→右流水线：节点 = 刷新阶段（顺序 = 执行顺序），节点间连线在上游阶段完成
// 后点亮。一个节点内部可能有多路并行子操作，以 done/total 计数表达。
// 节点 flex-1 弹性、连接线限宽（min-w-3 max-w-10）——窄容器节点收窄折行，
// 宽容器连接线伸展，任何宽度都不裁切
export function RefreshPipeline({ stages }: { stages: PcRefreshStageState[] }) {
  return (
    <div className='flex w-full items-start'>
      {stages.map((s, i) => (
        <Fragment key={s.key}>
          {i > 0 && (
            <div className='mt-3.5 min-w-3 max-w-10 flex-1 px-0.5'>
              <div
                className={cn(
                  'h-0.5 rounded-full transition-colors duration-500',
                  stages[i - 1].status === 'done' || stages[i - 1].status === 'skipped'
                    ? 'bg-primary'
                    : 'bg-border'
                )}
              />
            </div>
          )}
          <StageNode stage={s} index={i} />
        </Fragment>
      ))}
    </div>
  )
}

// 流水线卡片：聚合标题行 + 流水线 + 总进度条 + 当前活动行。页面进度条与刷新
// 弹窗共用；status.stages 缺失时（旧后端/无数据）退化为简易文本进度
export function RefreshPipelineCard({
  status,
  className,
}: {
  status: PcRefreshStatus
  className?: string
}) {
  const { t } = useTranslation()
  const stages = status.stages ?? []
  const pct =
    status.total > 0 ? Math.min(100, (status.done / status.total) * 100) : 0
  const failed = stages.filter((s) => s.status === 'failed').length
  return (
    <div
      className={cn(
        'bg-muted/50 text-muted-foreground rounded-md p-3 text-xs',
        className
      )}
    >
      <div className='flex items-center gap-2'>
        {status.running ? (
          <>
            <Loader2 className='text-primary h-3.5 w-3.5 animate-spin' />
            <span className='text-foreground font-medium'>
              {t('Refreshing…')}
            </span>
          </>
        ) : (
          <>
            <Check
              className={cn(
                'h-3.5 w-3.5',
                failed > 0 ? 'text-destructive' : 'text-primary'
              )}
            />
            <span className='text-foreground font-medium'>
              {failed > 0
                ? t('Refresh completed with {{count}} failed step(s)', {
                    count: failed,
                  })
                : t('Refresh completed')}
            </span>
          </>
        )}
        {status.total > 0 && (
          <span className='tabular-nums'>
            {Math.round(status.done)}/{Math.round(status.total)}
          </span>
        )}
      </div>
      {stages.length > 0 ? (
        <div className='mt-3'>
          <RefreshPipeline stages={stages} />
        </div>
      ) : (
        status.current && (
          <div className='mt-2 truncate'>· {status.current}</div>
        )
      )}
      <div className='bg-background mt-2 h-1 overflow-hidden rounded-full'>
        <div
          className='bg-primary h-full rounded-full transition-all'
          style={{ width: `${pct}%` }}
        />
      </div>
      {status.running && status.current && stages.length > 0 && (
        <div className='mt-1.5 truncate' title={status.current}>
          {status.current}
        </div>
      )}
    </div>
  )
}
