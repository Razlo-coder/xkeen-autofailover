import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { IconActivityHeartbeat } from '@tabler/icons-react'
import { ServerCard } from './serverCard'
import { ServerGroupCard } from './serverGroupCard'
import type { Server, SubscriptionInfo } from '@/types'

const PAGE_SIZE = 12
type Source = SubscriptionInfo['sources'][number]
type Entry =
    | { kind: 'server'; server: Server }
    | { kind: 'group'; name: string; source: number; members: Server[] }

function entriesForSource(servers: Server[], groupEnabled: boolean): Entry[] {
    const groups = new Map<string, Server[]>()
    if (groupEnabled) {
        for (const server of servers) {
            if (!server.group_name) continue
            groups.set(server.group_name, [
                ...(groups.get(server.group_name) ?? []),
                server,
            ])
        }
    }
    const seen = new Set<string>()
    const entries: Entry[] = []
    for (const server of servers) {
        const name = server.group_name
        const members = name ? groups.get(name) : undefined
        if (name && members && members.length > 1) {
            if (seen.has(name)) continue
            seen.add(name)
            entries.push({
                kind: 'group',
                name,
                source: server.source_id,
                members,
            })
        } else {
            entries.push({ kind: 'server', server })
        }
    }
    const active = (entry: Entry) =>
        entry.kind === 'group'
            ? entry.members.some(member => member.active)
            : entry.server.active
    const latency = (entry: Entry) => {
        const members = entry.kind === 'group' ? entry.members : [entry.server]
        const known = members
            .map(member => member.latency_ms)
            .filter(value => value >= 0)
        return known.length ? Math.min(...known) : Number.POSITIVE_INFINITY
    }
    return entries.sort((a, b) => {
        if (active(a) !== active(b)) return active(a) ? -1 : 1
        return latency(a) - latency(b)
    })
}

function SourceSection({
    source,
    servers,
    groupEnabled,
    onSelect,
    onSelectGroup,
    onSetCountry,
    loading,
}: {
    source: Source
    servers: Server[]
    groupEnabled: boolean
    onSelect: (id: number) => void
    onSelectGroup: (source: number, group: string) => void
    onSetCountry?: (id: number, country: string) => void
    loading: boolean
}) {
    const [collapsed, setCollapsed] = useState(false)
    const [visibleCount, setVisibleCount] = useState(PAGE_SIZE)
    const sentinelRef = useRef<HTMLDivElement>(null)
    const entries = entriesForSource(servers, groupEnabled)
    const visible = entries.slice(0, visibleCount)
    const hasMore = visibleCount < entries.length

    useEffect(() => {
        if (collapsed || !hasMore) return
        const el = sentinelRef.current
        if (!el) return
        const obs = new IntersectionObserver(
            items => {
                if (items[0].isIntersecting) setVisibleCount(c => c + PAGE_SIZE)
            },
            { rootMargin: '300px' },
        )
        obs.observe(el)
        return () => obs.disconnect()
    }, [collapsed, hasMore, visible.length])

    return (
        <section className='space-y-3'>
            <button
                type='button'
                onClick={() => setCollapsed(value => !value)}
                aria-expanded={!collapsed}
                className='flex w-full items-center justify-between rounded-lg border bg-card px-4 py-3 text-left hover:bg-muted/30'
            >
                <span className='font-medium'>
                    {source.name || `Подписка ${source.id + 1}`}
                    <span className='ml-2 text-sm font-normal text-muted-foreground'>
                        {entries.length} карточек · {servers.length} узлов
                    </span>
                </span>
                <span className='text-muted-foreground'>
                    {collapsed ? 'Развернуть ▾' : 'Свернуть ▴'}
                </span>
            </button>
            {!collapsed && (
                <>
                    <div className='grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-3'>
                        {visible.map(entry =>
                            entry.kind === 'group' ? (
                                <ServerGroupCard
                                    key={`${source.id}:${entry.name}`}
                                    name={entry.name}
                                    members={entry.members}
                                    onSelect={() =>
                                        onSelectGroup(entry.source, entry.name)
                                    }
                                    loading={loading}
                                />
                            ) : (
                                <ServerCard
                                    key={entry.server.id}
                                    server={entry.server}
                                    onSelect={onSelect}
                                    onSetCountry={onSetCountry}
                                    loading={loading}
                                />
                            ),
                        )}
                    </div>
                    {hasMore && <div ref={sentinelRef} className='h-8' />}
                </>
            )}
        </section>
    )
}

export function ServerList({
    servers,
    sources,
    groupEnabled,
    onSelect,
    onSelectGroup,
    onSetCountry,
    onCheckAll,
    loading,
    checking = false,
}: {
    servers: Server[]
    sources: Source[]
    groupEnabled: boolean
    onSelect: (id: number) => void
    onSelectGroup: (source: number, group: string) => void
    onSetCountry?: (id: number, country: string) => void
    onCheckAll: () => void
    loading: boolean
    checking?: boolean
}) {
    const sourceList = sources.length
        ? sources.filter(
              source =>
                  source.url || servers.some(s => s.source_id === source.id),
          )
        : [...new Set(servers.map(server => server.source_id))].map(id => ({
              id,
              name: '',
              detected_name: '',
              custom_name: '',
              url: '',
              last_updated: '',
              server_count: servers.filter(server => server.source_id === id)
                  .length,
          }))

    return (
        <div className='space-y-4'>
            <div className='flex items-center justify-between'>
                <h2 className='text-base font-semibold'>
                    Серверы ({servers.length})
                </h2>
                <Button
                    variant='outline'
                    size='sm'
                    onClick={onCheckAll}
                    disabled={loading || checking}
                >
                    <IconActivityHeartbeat className='size-4' />
                    {checking ? 'Пинг...' : 'Пинг'}
                </Button>
            </div>
            {sourceList.map(source => (
                <SourceSection
                    key={source.id}
                    source={source}
                    servers={servers.filter(
                        server => server.source_id === source.id,
                    )}
                    groupEnabled={groupEnabled}
                    onSelect={onSelect}
                    onSelectGroup={onSelectGroup}
                    onSetCountry={onSetCountry}
                    loading={loading}
                />
            ))}
            {servers.length === 0 && (
                <div className='py-8 text-center text-sm text-muted-foreground'>
                    Нет серверов. Добавьте подписку.
                </div>
            )}
        </div>
    )
}
