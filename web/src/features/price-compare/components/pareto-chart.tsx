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
import { ExternalLink } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'

import { Badge } from '@/components/ui/badge'
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
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useTranslation } from 'react-i18next'

import { PC_CATS, PC_SOURCES, pcCatLabel, pcDiscoverDims, pcEmptyStateText, pcSourceDef, pcSourceIsDynamic, pcSourceOf } from '../use-price-compare'
import type { PcDim, PriceCompareState } from '../use-price-compare'
import type { PcUnifiedRow } from '../types'
import { PC_CHANNEL_META, PC_PERIOD_LABEL, PcMoney, fmtLatency, pcPeriodNowKey } from './pc-ui'

interface Pt {
  row: PcUnifiedRow
  price: number
  score: number // 参与帕累托/坐标计算的值（rank 维度取负：排名越小越好，统一成"越大越好"）
  disp: number // 展示用原始值
}

// 前沿标签精确宽度（canvas measureText，字体同 body）：几何全按 1:1 像素计算。
// 必须按 700 字重量——悬浮高亮时文字加粗（fontWeight 700），按 400 量宽会让底板放不下
let pcMeasureCtx: CanvasRenderingContext2D | null = null
function pcMeasureLabel(nm: string): number {
  if (!pcMeasureCtx) pcMeasureCtx = document.createElement('canvas').getContext('2d')
  if (!pcMeasureCtx) return nm.length * 6.6
  pcMeasureCtx.font = `700 12px ${getComputedStyle(document.body).fontFamily}`
  return pcMeasureCtx.measureText(nm).width
}

const CHART_COLORS: Record<string, string> = {
  openrouter: 'var(--chart-1, #3b82f6)',
  opencode: 'var(--chart-4, #f59e0b)',
  deepseek: 'var(--chart-2, #10b981)',
  // 中转渠道用图例（PC_CHANNEL_META.dot）同色，圆点与图例对得上
  apib: '#8b5cf6',
  buzzai: '#06b6d4',
  apikl: '#f43f5e',
  ikun: '#84cc16',
}

export function ParetoChart({
  pc,
  onOpenModel,
}: {
  pc: PriceCompareState
  onOpenModel: (key: string) => void
}) {
  const { t } = useTranslation()
  // active = 高亮点（pts 下标），tip = 悬浮详情位置；二者分离：排名表行悬浮只
  // 高亮对应点不出弹层，鼠标一离开点/表格立即收起弹层（修复弹层滞留）
  const [active, setActive] = useState<number | null>(null)
  const [tip, setTip] = useState<{ x: number; y: number } | null>(null)

  const scoreSource = pcSourceOf(pc.scoreDim)
  const srcDef = pcSourceDef(scoreSource)
  const benchMeta = pc.unified?.data?.bench_meta
  const dynDims = useMemo(
    () =>
      pcSourceIsDynamic(scoreSource)
        ? pcDiscoverDims(scoreSource, pc.rows, benchMeta ?? {})
        : [],
    [scoreSource, pc.rows, benchMeta]
  )
  const dims: PcDim[] = pcSourceIsDynamic(scoreSource)
    ? dynDims
    : (srcDef?.dims ?? [])
  // 三级导航中间层：分类（编程/数学/推理/智能体…）。子榜单多（≥12）且分类 ≥2 才
  // 启用，小榜单保持两行不过度分层；主维度（第一维度）所在分类恒排最前
  const [cat, setCat] = useState<string | null>(null)
  const cats = useMemo(() => {
    const seen: string[] = []
    dims.forEach((d) => {
      const c = d.cat || 'other'
      if (!seen.includes(c)) seen.push(c)
    })
    const primary = dims[0]?.cat || 'other'
    const idx = (c: string) => PC_CATS.findIndex((x) => x.key === c)
    return seen.sort((a, b) =>
      a === primary ? -1 : b === primary ? 1 : idx(a) - idx(b)
    )
  }, [dims])
  const grouped = cats.length >= 2 && dims.length >= 12
  // 切换榜单源后重置分类：旧源选中的分类不自动延续到新源（activeCat 兜底跟随
  // 当前维度的分类），否则分类行停在旧分类、子榜单没有选中项而图画的却是另一维度
  useEffect(() => {
    setCat(null)
  }, [scoreSource])
  const activeCat =
    cat && cats.includes(cat)
      ? cat
      : (dims.find((d) => d.key === pc.scoreDim)?.cat ??
        cats[0] ??
        'overall')
  const shownDims = grouped
    ? dims.filter((d) => (d.cat || 'other') === activeCat)
    : dims
  const switchCat = (c: string) => {
    setCat(c)
    const first = dims.find((d) => (d.cat || 'other') === c)
    if (first && first.key !== pc.scoreDim) pc.changeScoreDim(first.key)
  }
  const srcLabel = srcDef?.label ?? scoreSource
  const dimLabel = dims.find((d) => d.key === pc.scoreDim)?.label ?? pc.scoreDim
  const isArena = scoreSource === 'arena'
  // Elo 分与净提升%/排名量纲不同：Elo 用纯数值，净提升带 %，排名越小越好（内部取负）
  const isEloDim = pc.scoreDim.endsWith('_elo')
  const isRankDim =
    (dims.find((d) => d.key === pc.scoreDim)?.rank ?? false) ||
    pc.scoreDim.endsWith('_rank')

  const pts = useMemo<Pt[]>(
    () =>
      pc.rows
        .filter((r) => pc.channels[r.channel] !== false && !r.dead && pc.scoreOf(r) != null)
        .map((r) => {
          const raw = pc.scoreOf(r) as number
          return {
            row: r,
            price: Math.max(pc.actualOf(r), 0.001),
            score: isRankDim ? -raw : raw,
            disp: raw,
          }
        })
        .sort((a, b) => a.price - b.price || b.score - a.score),
    [pc.rows, pc.channels, pc.scoreOf, pc.actualOf, isRankDim]
  )
  // pts 重建（unified 并发刷新/渠道与维度切换）后旧 active 下标可能越界或指向
  // 另一行：复位悬停态，避免渲染期 pts[active] 抛 TypeError、tooltip 张冠李戴
  useEffect(() => {
    setActive(null)
    setTip(null)
  }, [pts])

  const frontIdx = useMemo(() => {
    const idx: number[] = []
    let mx = -Infinity
    pts.forEach((p, i) => {
      if (p.score > mx) {
        idx.push(i)
        mx = p.score
      }
    })
    return idx
  }, [pts])
  const frontSet = useMemo(() => new Set(frontIdx), [frontIdx])
  // 因缺失当前维度得分而未绘制的报价数（避免"某某模型去哪了"的困惑）
  const hiddenCount = useMemo(
    () =>
      pc.rows.filter((r) => pc.channels[r.channel] !== false && pc.scoreOf(r) == null)
        .length,
    [pc.rows, pc.channels, pc.scoreOf]
  )

  // 图表按容器实际像素 1:1 绘制（ResizeObserver 量宽）：图内文字 = 真实 CSS 像素，
  // 与页面文字同大——用 viewBox+w-full 拉伸时图内文字会随容器缩放，比例永远不对。
  // 依赖 pts.length>0：容器在数据就绪后才挂载，届时需重新挂观察器量一次真实宽度
  const boxRef = useRef<HTMLDivElement>(null)
  const [boxW, setBoxW] = useState(980)
  useEffect(() => {
    const el = boxRef.current
    if (!el) return
    const ro = new ResizeObserver(() => setBoxW(el.clientWidth))
    ro.observe(el)
    setBoxW(el.clientWidth)
    return () => ro.disconnect()
  }, [pts.length > 0])
  const W = Math.max(320, boxW)
  const H = 440
  const L = 58
  const R = 28 // 小右缘：标签贴点放置自会钳制，无需旧版的宽翻转余量
  const T = 18
  const B = 46

  const geo = useMemo(() => {
    if (pts.length === 0) return null
    const xs = pts.map((p) => p.price)
    const lg = (v: number) => Math.log10(v)
    // 对数域两端各留 3% decade 余量，最边上的圆点不再压到轴线/出界
    const xmin = Math.pow(10, Math.floor(lg(Math.min(...xs))) - 0.03)
    const xmax = Math.pow(10, Math.ceil(lg(Math.max(...xs))) + 0.03)
    const scores = pts.map((p) => p.score)
    const lo = Math.min(...scores)
    const hi = Math.max(...scores)
    // 步长随量纲自适应：Elo 千位段 50，百分段 10，个位段 5/1，小数段 0.5
    const span = hi - lo
    const step = span >= 300 ? 50 : span >= 30 ? 10 : span >= 5 ? 5 : span >= 2 ? 1 : 0.5
    const smin = Math.floor((lo - 1) / step) * step
    // 顶部留 ≥0.6 步长余量，最高分的点不顶到绘图区上缘
    const smax = Math.ceil((hi + Math.max(1, step * 0.6)) / step) * step
    const lx = (v: number) =>
      L + ((lg(v) - lg(xmin)) / (lg(xmax) - lg(xmin) || 1)) * (W - L - R)
    const ly = (v: number) =>
      T + (1 - (v - smin) / (smax - smin || 1)) * (H - T - B)
    return { xmin, xmax, smin, smax, step, lx, ly }
  }, [pts, W])

  // 前沿标签：贴点放在点右下方（前沿线从左下上来，右下是空白区），相邻碰撞时
  // 向下顺延，接近右缘翻转到点左下，全程钳制在绘图区内。标签宽度用 canvas
  // measureText 精确量（svg 已是 1:1 像素渲染、字体与 body 一致）——按字符数
  // 估算偏宽会制造假碰撞，把标签级联推离圆点
  const labelPos = useMemo(() => {
    if (!geo) return []
    const placed: { x: number; y: number; w: number; h: number }[] = []
    return frontIdx.map((i) => {
      const p = pts[i]
      const nm =
        p.row.name.length > 24 ? p.row.name.slice(0, 23) + '…' : p.row.name
      const tw = pcMeasureLabel(nm) // 纯文字宽（矩形底板居中用）
      const w = tw + 4 // 碰撞检测宽（略放宽防贴字）
      const px = geo.lx(p.price)
      const py = geo.ly(p.score)
      let y = py + 16
      let anchor: 'start' | 'end' = 'start'
      let x = px + 7
      let free = false
      const slots: { a: 'start' | 'end'; left: number; y: number }[] = []
      for (let s = 0; s < 4; s++) {
        const below = py + 16 + 13 * s
        const above = py - 6 - 13 * s
        slots.push({ a: 'start', left: px + 7, y: below })
        slots.push({ a: 'start', left: px + 7, y: above })
        slots.push({ a: 'end', left: px - 7 - w, y: below })
        slots.push({ a: 'end', left: px - 7 - w, y: above })
      }
      for (const sl of slots) {
        if (sl.left < L + 2 || sl.left + w > W - R - 2) continue
        if (sl.y - 9 < T || sl.y + 3 > H - B) continue
        const by = sl.y - 9
        const hit = placed.some(
          (q) =>
            sl.left < q.x + q.w &&
            sl.left + w > q.x &&
            by < q.y + q.h &&
            by + 10 > q.y
        )
        if (!hit) {
          anchor = sl.a
          x = sl.a === 'start' ? sl.left : sl.left + w
          y = sl.y
          free = true
          break
        }
      }
      if (!free) {
        anchor = 'start'
        x = px + 7
        y = py + 16
      }
      y = Math.min(y, H - B - 2)
      placed.push({ x: anchor === 'start' ? x : x - w, y: y - 9, w, h: 10 })
      return { i, nm, anchor, x, y, tw }
    })
  }, [pts, frontIdx, geo, W])

  // 排名表数据：沿前沿线从上（分最高=效果最好）到下排列，只含前沿上的模型
  const frontSorted = useMemo(
    () =>
      frontIdx
        .map((i) => ({ i, p: pts[i] }))
        .sort((a, b) => b.p.score - a.p.score),
    [pts, frontIdx]
  )

  // 得分统一保留 2 位有效小数（AA 指数等原始值是长浮点，会撑宽排名表把价格列挤出可视区）
  const fmtScore = (v: number) => {
    const s = Number(v.toFixed(2))
    return isRankDim ? `#${s}` : `${s}${isArena && !isEloDim ? '%' : ''}`
  }

  const curOf = (c: string) => (c === 'CNY' ? '¥' : '$')
  // 原始单价：输入/输出/缓存读/缓存写（缓存项仅在 >0 时显示，小值保留 3 位小数）
  const priceBreakdown = (r: PcUnifiedRow) => {
    const cur = curOf(r.currency)
    const parts = [
      `${t('Input')} ${cur}${r.in.toFixed(2)}`,
      `${t('Output')} ${cur}${r.out.toFixed(2)}`,
    ]
    if (r.read > 0)
      parts.push(`${t('Cache read')} ${cur}${Number(r.read.toFixed(3))}`)
    if (r.write > 0)
      parts.push(`${t('Cache write')} ${cur}${r.write.toFixed(2)}`)
    return parts.join(' · ')
  }

  const tipHtml = (pt: Pt) => {
    const r = pt.row
    const dimVal = fmtScore(pt.disp)
    // code/overall 两榜独立匹配（code 可为 null 而 overall 命中），且 rank 解析
    // 不到时后端写 null：任一榜有有效排名才展示（优先 overall），不渲染 "#null"
    const arenaRank = r.arena?.overall?.rank ?? r.arena?.code?.rank ?? null
    const arenaPart = arenaRank != null ? ` · Arena #${arenaRank}` : ''
    const lbPart =
      r.lb && r.lb.average != null
        ? `${t('LiveBench avg')} ${Number(r.lb.average).toFixed(1)}`
        : arenaRank != null
          ? `${t('Arena best')} #${arenaRank}`
          : `${t('LiveBench avg')} —`
    return `${dimVal} · ${lbPart}${arenaPart}`
  }

  // 悬浮详情避开标签底板：默认紧贴标签上沿之上；标签贴近图表顶部放不下时，
  // 改到标签下沿之下——任何情况下都不遮挡底板与圆点
  const activeLab = active != null ? labelPos.find((l) => l.i === active) : undefined
  // pts 重建与上方复位 effect 生效之间会有一帧旧下标：先取安全引用，越界即 undefined
  const activePt = active != null ? pts[active] : undefined
  let tipTop = 10
  if (activeLab) {
    const aboveTop = activeLab.y - 14 - 176
    tipTop = aboveTop >= 8 ? aboveTop : activeLab.y + 12
  } else if (activePt && geo) {
    tipTop = Math.min(geo.ly(activePt.score) + 10, H - 165)
  }

  return (
    <Card>
      <CardHeader>
        <div className='flex items-start justify-between gap-2'>
          <CardTitle>{t('Pareto frontier')}</CardTitle>
          {srcDef?.url && (
            <a
              href={srcDef.url}
              target='_blank'
              rel='noreferrer'
              className='text-muted-foreground hover:text-foreground inline-flex flex-none items-center gap-1 text-xs transition-colors'
            >
              {t(srcLabel)}
              <ExternalLink className='h-3 w-3' />
            </a>
          )}
        </div>
        <CardDescription>
          {t(
            'X axis: actual price (¥/M, log) · Y axis: {{source}} score · hover for details, click for cross-channel offers',
            { source: t(srcLabel) }
          )}
        </CardDescription>
        <div className='space-y-1.5 pt-1'>
          {/* 三行卡片式切换：榜单来源 → 分类（大榜单才有）→ 该分类下的子榜单 */}
          <Tabs
            value={scoreSource}
            onValueChange={(v) => v && pc.changeScoreSource(v as string)}
          >
            <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
              {PC_SOURCES.map((sd) => (
                <TabsTrigger
                  key={sd.key}
                  value={sd.key}
                  className='flex-none px-3 py-1 text-xs font-bold'
                >
                  {t(sd.label)}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          {grouped && (
            <Tabs
              value={activeCat}
              onValueChange={(v) => v && switchCat(v)}
            >
              <TabsList className='bg-muted/40 max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
                {cats.map((c) => (
                  <TabsTrigger
                    key={c}
                    value={c}
                    className='flex-none px-2.5 py-0.5 text-xs font-semibold'
                  >
                    {t(pcCatLabel(c))}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          )}
          {shownDims.length > 0 && (
            <Tabs
              value={pc.scoreDim}
              onValueChange={(v) => v && pc.changeScoreDim(v as string)}
            >
              <TabsList className='bg-muted/60 max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
                {shownDims.map((d) => (
                  <TabsTrigger
                    key={d.key}
                    value={d.key}
                    className='flex-none px-2.5 py-0.5 text-xs'
                  >
                    {t(d.label)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          )}
        </div>
      </CardHeader>
      <CardContent className='relative'>
        {!geo ? (
          <div className='text-muted-foreground py-16 text-center text-sm'>
            {pcEmptyStateText(pc.unified.data, t, pc.unified.error)}
          </div>
        ) : (
          <div className='relative flex items-stretch gap-3'>
            {/* 图表列 = svg + 图例：图例属于图表列，两列底边才可能齐平 */}
            <div className='flex min-w-0 flex-1 flex-col pr-[348px]'>
              <div className='relative' ref={boxRef}>
              <svg
                width={W}
                height={H}
                viewBox={`0 0 ${W} ${H}`}
                className='block'
                onMouseLeave={() => {
  setActive(null)
  setTip(null)
  }}
  >
      {(() => {
      // 文本内容转义：撇号等经 .replace(/'/g,'"') 后会破坏文字（如 Humanity"s），
      // 外部数据（LLM Stats 基准名）也不能裸进 innerHTML
      const esc = (v: string | number) =>
      String(v)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/'/g, '&#39;')
      let s = ''
      for (
      let d = Math.round(Math.log10(geo.xmin));
      d <= Math.round(Math.log10(geo.xmax));
      d++
      ) {
      const x = geo.lx(Math.pow(10, d))
      s += `<line x1='${x}' y1='${T}' x2='${x}' y2='${H - B}' stroke='var(--border)' stroke-opacity='0.6'/>`
      s += `<text x='${x}' y='${H - B + 17}' text-anchor='middle' font-size='12' fill='var(--muted-foreground)'>${Math.pow(10, d)}</text>`
      }
      for (let v = geo.smin; v <= geo.smax; v += geo.step) {
        const tv = isRankDim ? -v : v
        // rank 维度取负后的轴两端余量会落到合法排名域（≥1 的整数）之外：
        // 0/负数/小数都不可能是真实排名，网格线与刻度标签一并跳过
        if (isRankDim && (tv < 1 || !Number.isInteger(tv))) continue
        const y = geo.ly(v)
        s += `<line x1='${L}' y1='${y}' x2='${W - R}' y2='${y}' stroke='var(--border)' stroke-opacity='0.5'/>`
        s += `<text x='${L - 7}' y='${y + 4}' text-anchor='end' font-size='12' fill='var(--muted-foreground)'>${Number.isInteger(tv) ? tv : tv.toFixed(1)}</text>`
      }
      s += `<text x='${W - R}' y='${H - B + 32}' text-anchor='end' font-size='12' fill='var(--muted-foreground)'>${esc(t('Actual price ¥/M (log)'))} →</text>`
      // 纵轴标题旋转竖排、沿绘图区垂直居中，避免横排压住刻度数字
      const yMid = ((T + (H - B)) / 2).toFixed(1)
      s += `<text transform='rotate(-90 12 ${yMid})' x='12' y='${yMid}' text-anchor='middle' font-size='12' fill='var(--muted-foreground)'>${esc(t(dimLabel))}</text>`
      return (
      <g
      dangerouslySetInnerHTML={{
      __html: s.replace(/'/g, '"'),
      }}
      />
      )
      })()}
      {/* 绘制层级：弱化的非前沿点 → 加粗前沿线 → 前沿点最顶（用户聚焦前沿） */}
      {pts.map((p, i) =>
      frontSet.has(i) ? null : (
      <circle
      key={i}
      className='cursor-pointer'
      cx={geo!.lx(p.price)}
      cy={geo!.ly(p.score)}
      r={active === i ? 6 : 4}
      fill={CHART_COLORS[p.row.channel]}
      stroke={active === i ? 'var(--foreground)' : 'var(--background)'}
      strokeWidth={active === i ? 1.8 : 1.2}
      opacity={active === i ? 0.95 : 0.45}
      onMouseEnter={() => {
        // svg 1:1 像素渲染（见上），tip 坐标直接用几何值，与标签悬浮同一公式
        setTip({
          x: geo!.lx(p.price) + 14,
          y: geo!.ly(p.score) + 10,
        })
        setActive(i)
      }}
      onMouseLeave={() => {
      setTip(null)
      setActive(null)
      }}
      onClick={() => onOpenModel(p.row.key)}
      />
      )
      )}
      {frontIdx.length > 1 && (
      <path
      d={frontIdx
      .map(
      (i, k) =>
      `${k ? 'L' : 'M'}${geo!.lx(pts[i].price).toFixed(1)},${geo!.ly(pts[i].score).toFixed(1)}`
      )
      .join(' ')}
      fill='none'
      stroke='var(--chart-5, #ec4899)'
      strokeWidth='3'
      opacity='0.9'
      />
      )}
      {frontIdx.map((i) => {
      const p = pts[i]
      const isActive = active === i
      return (
      <circle
      key={i}
      className='cursor-pointer'
      cx={geo!.lx(p.price)}
      cy={geo!.ly(p.score)}
      r={isActive ? 8 : 6}
      fill={CHART_COLORS[p.row.channel]}
      stroke={isActive ? 'var(--foreground)' : 'var(--background)'}
        strokeWidth={isActive ? 2 : 1.4}
        onMouseEnter={() => {
          setTip({
            x: geo!.lx(p.price) + 14,
            y: geo!.ly(p.score) + 10,
          })
          setActive(i)
        }}
      onMouseLeave={() => {
      setTip(null)
      setActive(null)
      }}
      onClick={() => onOpenModel(p.row.key)}
      />
      )
      })}
            {labelPos.map(({ i, nm, anchor, x, y, tw }) => {
              // 半透明矩形底板与主题反色（浅色主题=深色底板，深色主题=浅色底板）：
                // 底板用 foreground、文字用 background，二者互为反色自动跟随主题。
                // 底板按纯文字宽左右对称留白 4px、垂直对齐字型视觉中心（略降 1px）。
                // 标签整体可悬浮：高亮圆点+底板加深+弹详情，点击开模型弹窗（同圆点）
              const textLeft = anchor === 'end' ? x - tw : x
              return (
                <g
                  key={`l${i}`}
                  className='cursor-pointer select-none'
                  onMouseEnter={() => {
                    setTip({
                      x: geo!.lx(pts[i].price) + 14,
                      y: geo!.ly(pts[i].score) + 10,
                    })
                    setActive(i)
                  }}
                  onMouseLeave={() => {
                    setTip(null)
                    setActive(null)
                  }}
                  onClick={() => onOpenModel(pts[i].row.key)}
                >
                  <rect
                    x={textLeft - 4}
                    y={y - 14}
                    width={tw + 8}
                    height={18}
                    rx={3}
                    fill={`color-mix(in srgb, var(--foreground) ${active === i ? 80 : 55}%, transparent)`}
                  />
                  <text
                    x={x}
                    y={y}
                    fontSize={12}
                    fontWeight={active === i ? 700 : 400}
                    fill='var(--background)'
                    textAnchor={anchor}
                  >
                    {nm}
                  </text>
                </g>
              )
            })}
      </svg>
  {tip && activePt && (
  <div
  className='bg-popover text-popover-foreground pointer-events-none absolute z-30 min-w-[230px] rounded-lg border p-2.5 text-xs leading-relaxed shadow-md'
  style={{
  left: `min(${tip.x}px, calc(100% - 244px))`,
  top: `${tipTop}px`,
  }}
  >
  {(() => {
  const r = activePt.row
  const price = activePt.price
  return (
  <>
  <div className='flex flex-wrap items-center gap-1.5 font-semibold'>
  {r.name}
  <Badge variant='outline' className='px-1 py-0 text-[10px]'>
  {t(PC_CHANNEL_META[r.channel]?.label || '')}
  </Badge>
  {r.period && (
  <Badge variant='outline' className='px-1 py-0 text-[10px]'>
  {t(PC_PERIOD_LABEL[r.period] ?? r.period)}
  {r.period === pcPeriodNowKey()
  ? ` · ${t('now')}`
  : ''}
  </Badge>
  )}
  </div>
  <div className='text-muted-foreground'>
  {[r.vendor?.name, r.provider, r.quant]
  .filter((v) => v && v !== '—' && v !== 'unknown')
  .join(' · ')}
  </div>
  <div>
  {t('Actual price')} <b>¥{price.toFixed(3)}</b>
  {t('/M input')}
  </div>
  <div>{priceBreakdown(r)}</div>
  <div>{tipHtml(activePt)}</div>
  <div className='text-muted-foreground'>
  {r.throughput != null
  ? `${Number(r.throughput).toLocaleString()} tok/s`
  : '—'}
  {r.latency != null
  ? ` · ${t('Latency')} ${fmtLatency(Number(r.latency))}`
  : ''}
  </div>
  {r.note && (
  <div className='text-muted-foreground'>{r.note}</div>
  )}
  </>
  )
  })()}
  </div>
  )}
              </div>
              <div className='text-muted-foreground mt-2 flex flex-none flex-wrap gap-4 text-xs'>
                {Object.entries(PC_CHANNEL_META).map(([k, v]) => (
                  <span key={k} className='inline-flex items-center gap-1.5'>
                    <span
                      className='inline-block h-2.5 w-2.5 rounded-full'
                      style={{ background: v.dot }}
                    />
                    {t(v.label)}
                  </span>
                ))}
                <span className='inline-flex items-center gap-1.5'>
                  <svg width='26' height='8'>
                    <line
                      x1='0'
                      y1='4'
                      x2='26'
                      y2='4'
                      stroke='var(--chart-5, #ec4899)'
                      strokeWidth='2'
                    />
                  </svg>
                  {t('Pareto frontier')}
                </span>
                <span>
                  {t('{{count}} scored offers', { count: pts.length })}
                  {hiddenCount > 0
                    ? ` · ${t('{{count}} offers lack this metric, not shown', { count: hiddenCount })}`
                    : ''}
                  {isArena
                    ? ` · ${
                        isEloDim
                          ? t('Arena Y axis = Elo score, higher is better')
                          : isRankDim
                            ? t('Arena Y axis = rank, lower is better')
                            : t('Arena Y axis = Net Improvement %, higher is better')
                      }`
                    : ''}
                </span>
              </div>
            </div>
            {/* 右侧排名表：只列前沿上的模型，沿前沿线从上（最好）到下（最差）。
                高度严格随图：滚动区 h-0+flex-1（固有高度不参与行高），行高只由图表
                决定，底边对齐；样式与站内表格一致（无边框盒/默认行距/selected 高亮） */}
            <aside className='absolute inset-y-0 right-0 flex w-[336px] flex-col'>
              <div className='text-muted-foreground mb-1 flex flex-none items-center justify-between text-xs font-medium'>
                <span>{t('Frontier ranking')}</span>
                <span className='tabular-nums'>{frontSorted.length}</span>
              </div>
              <div className='h-0 min-h-0 flex-1 overflow-y-auto'>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className='w-10'>#</TableHead>
                      <TableHead>{t('Model')}</TableHead>
                      <TableHead className='text-right'>{t('Score')}</TableHead>
                      <TableHead className='text-right'>
                        {t('Actual ¥/M')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {frontSorted.map(({ i, p }, rank) => (
                      <TableRow
                        key={i}
                        data-state={active === i ? 'selected' : undefined}
                        className='cursor-pointer'
                        onMouseEnter={() => setActive(i)}
                        onMouseLeave={() => setActive(null)}
                        onClick={() => onOpenModel(p.row.key)}
                      >
                        <TableCell className='text-muted-foreground'>
                          {rank + 1}
                        </TableCell>
                        <TableCell
                          className='max-w-[236px] truncate'
                          title={p.row.name}
                        >
                          {p.row.name}
                        </TableCell>
                        <TableCell className='text-right font-mono'>
                          {fmtScore(p.disp)}
                        </TableCell>
                        <TableCell className='text-right'>
                          <PcMoney value={p.price} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <div className='text-muted-foreground mt-1 flex-none text-[11px]'>
                {t('Ranked along the Pareto frontier from best to worst')}
              </div>
            </aside>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
