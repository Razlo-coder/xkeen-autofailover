import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/hooks/useAuth'
import { useEventSource } from '@/hooks/useEventSource'
import { useStreamLatency } from '@/hooks/useStreamLatency'
import { api } from '@/lib/api'
import { StatusBadge } from '@/components/statusBadge'
import { ConnectionUptime } from '@/components/connectionUptime'
import { SubscriptionForm } from '@/components/subscriptionForm'
import { ServerList } from '@/components/serverList'
import { Controls } from '@/components/controls'
import { XKeenCard } from '@/components/xkeenCard'
import { SettingsCard } from '@/components/settingsCard'
import { PasskeyCard } from '@/components/passkeyCard'
import { AutomationCard, countryName } from '@/components/automationCard'
import { LogViewer } from '@/components/logViewer'
import { Button } from '@/components/ui/button'
import { IconLogout, IconLoader2 } from '@tabler/icons-react'
import type {
    Status,
    SubscriptionInfo,
    Server,
    SelfTestResult,
    PoolStatus,
    PoolSyncResult,
} from '@/types'

export function DashboardPage() {
    const { logout } = useAuth()
    const qc = useQueryClient()

    // SSE: status, logs and restart events in real time
    useEventSource()
    const {
        check: checkLatency,
        checking: checkingLatency,
        cancel: cancelLatency,
    } = useStreamLatency()

    const status = useQuery({
        queryKey: ['status'],
        queryFn: () => api.get<Status>('/api/status'),
    })

    const restarting = status.data?.restarting ?? false

    const subscription = useQuery({
        queryKey: ['subscription'],
        queryFn: () => api.get<SubscriptionInfo>('/api/subscription'),
    })

    const servers = useQuery({
        queryKey: ['servers'],
        queryFn: () =>
            api
                .get<{ servers: Server[] }>('/api/servers')
                .then(d => d.servers ?? []),
    })

    const pool = useQuery({
        queryKey: ['pool'],
        queryFn: () => api.get<PoolStatus>('/api/pool'),
    })

    const logs = useQuery({
        queryKey: ['logs'],
        queryFn: () =>
            api
                .get<{ lines: string[] }>('/api/logs?lines=50')
                .then(d => d.lines ?? []),
    })

    const updateSub = useMutation({
        mutationFn: ({ source, url }: { source: number; url: string }) =>
            api.post<{ servers: Server[] }>('/api/subscription', {
                source,
                url,
            }),
        onMutate: cancelLatency,
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['subscription'] })
            qc.invalidateQueries({ queryKey: ['servers'] })
        },
    })

    const refreshSub = useMutation({
        mutationFn: () =>
            api.post<{ servers: Server[] }>('/api/subscription/refresh'),
        onMutate: cancelLatency,
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['subscription'] })
            qc.invalidateQueries({ queryKey: ['servers'] })
        },
    })

    const selectServer = useMutation({
        mutationFn: (id: number) => api.post('/api/servers/select', { id }),
        onMutate: id => {
            cancelLatency()
            qc.setQueryData<Server[]>(['servers'], old =>
                old?.map(s => ({ ...s, active: s.id === id })),
            )
            qc.setQueryData<Status>(['status'], old =>
                old ? { ...old, restarting: true } : old,
            )
        },
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['servers'] })
            qc.invalidateQueries({ queryKey: ['status'] })
        },
    })

    const restart = useMutation({
        mutationFn: () => api.post('/api/xkeen/restart'),
        onMutate: () => {
            cancelLatency()
            qc.setQueryData<Status>(['status'], old =>
                old ? { ...old, restarting: true } : old,
            )
        },
        onSettled: () => qc.invalidateQueries({ queryKey: ['status'] }),
    })

    const start = useMutation({
        mutationFn: () => api.post('/api/xkeen/start'),
        onMutate: cancelLatency,
        onSettled: () => qc.invalidateQueries({ queryKey: ['status'] }),
    })

    const stop = useMutation({
        mutationFn: () => api.post<{ warning?: string }>('/api/xkeen/stop'),
        onMutate: cancelLatency,
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['status'] })
            qc.invalidateQueries({ queryKey: ['automation'] })
        },
    })

    const poolAction = useMutation({
        mutationFn: (action: 'enable' | 'disable' | 'sync') =>
            api.post<PoolSyncResult>(`/api/pool/${action}`),
        onMutate: () => {
            qc.setQueryData<Status>(['status'], old =>
                old ? { ...old, restarting: true } : old,
            )
        },
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['pool'] })
            qc.invalidateQueries({ queryKey: ['status'] })
        },
    })

    const syncMihomo = useMutation({
        mutationFn: () => api.post('/api/mihomo/sync'),
        onMutate: () => {
            qc.setQueryData<Status>(['status'], old =>
                old ? { ...old, restarting: true } : old,
            )
        },
        onSettled: () => qc.invalidateQueries({ queryKey: ['status'] }),
    })

    const setCountry = useMutation({
        mutationFn: ({ id, country }: { id: number; country: string }) =>
            api.post('/api/servers/country', { id, country }),
        onSettled: () => qc.invalidateQueries({ queryKey: ['servers'] }),
    })

    const toggleWatchdog = useMutation({
        mutationFn: (active: boolean) =>
            api.post('/api/watchdog/toggle', { active }),
        onMutate: active => {
            if (!active) cancelLatency()
            qc.setQueryData<Status>(['status'], old =>
                old ? { ...old, watchdog_active: active } : old,
            )
        },
        onSettled: () => {
            qc.invalidateQueries({ queryKey: ['status'] })
            qc.invalidateQueries({ queryKey: ['automation'] })
        },
    })

    const s = status.data

    return (
        <div className='min-h-screen'>
            {/* Баннер рестарта */}
            {restarting && (
                <div className='bg-amber-500/10 border-b border-amber-500/30 px-4 py-2.5 flex items-center justify-center gap-2 text-sm text-amber-400'>
                    <IconLoader2 className='size-4 animate-spin' />
                    XKeen перезапускается...
                </div>
            )}
            {/* Шапка */}
            <header className='bg-card border-b sticky top-0 z-10'>
                <div className='max-w-6xl mx-auto px-4 py-3 flex items-center justify-between gap-3'>
                    <div className='min-w-0'>
                        <h1 className='text-lg font-bold flex items-center gap-2'>
                            <img src='/favicon.svg' alt='' className='size-6' />
                            XKeen Panel
                        </h1>
                        <div className='flex flex-wrap items-center gap-x-3 gap-y-1 mt-0.5'>
                            <StatusBadge
                                connected={s?.connected ?? false}
                                xrayRunning={s?.xray_running ?? false}
                                latency={s?.latency_ms ?? -1}
                                qualityDegraded={s?.quality_degraded ?? false}
                            />
                            {s?.current_server && (
                                <span className='text-xs text-muted-foreground'>
                                    {subscription.data?.sources?.[1]?.url &&
                                        s.current_source_id !== undefined &&
                                        `№${s.current_source_id + 1} · `}
                                    {s.current_server}
                                    {s.protocol && ` (${s.protocol})`}
                                </span>
                            )}
                            <ConnectionUptime
                                since={s?.connected_since}
                                seconds={s?.uptime_seconds}
                                connected={s?.connected ?? false}
                                restarting={restarting}
                            />
                        </div>
                    </div>
                    <Button
                        className='shrink-0'
                        variant='outline'
                        size='sm'
                        onClick={logout}
                    >
                        <IconLogout className='size-4' />
                        Выйти
                    </Button>
                </div>
            </header>
            {/* Контент */}
            <main className='max-w-6xl mx-auto px-4 py-4'>
                {s?.verified_failover && (
                    <div className='mb-4 rounded-lg border p-4 text-sm space-y-1'>
                        <p className='font-semibold'>
                            Автоматическая замена сервера
                        </p>
                        <p>
                            Приоритет:{' '}
                            {s.country_priority?.length
                                ? s.country_priority
                                      .map(countryName)
                                      .join(' → ')
                                : 'порядок подписки'}
                            .
                            {s.allow_other_countries
                                ? ' Остальные страны также разрешены.'
                                : ' Используются только выбранные страны.'}
                        </p>
                        <p className='text-muted-foreground'>
                            Панель проверяет доступность и задержку через VPN.
                            Контроль качества и возврат к приоритетным серверам
                            настраиваются ниже. Замена применяется с проверкой и
                            откатом.
                        </p>
                    </div>
                )}
                {(selectServer.error ||
                    updateSub.error ||
                    refreshSub.error ||
                    restart.error ||
                    start.error ||
                    stop.error ||
                    stop.data?.warning ||
                    toggleWatchdog.error) && (
                    <p role='alert' className='mb-4 text-sm text-red-400'>
                        {selectServer.error?.message ||
                            updateSub.error?.message ||
                            refreshSub.error?.message ||
                            restart.error?.message ||
                            start.error?.message ||
                            stop.error?.message ||
                            stop.data?.warning ||
                            toggleWatchdog.error?.message}
                    </p>
                )}
                <div className='grid grid-cols-1 lg:grid-cols-[340px_1fr] gap-4'>
                    <div className='space-y-4'>
                        <SubscriptionForm
                            subscription={subscription.data ?? null}
                            onUpdate={(source, url) =>
                                updateSub.mutate({ source, url })
                            }
                            onRefresh={() => refreshSub.mutate()}
                            loading={
                                updateSub.isPending || refreshSub.isPending
                            }
                        />
                        {s?.verified_failover && (
                            <AutomationCard servers={servers.data ?? []} />
                        )}
                        <Controls
                            watchdogActive={s?.watchdog_active ?? false}
                            coreRunning={s?.xray_running ?? false}
                            stopping={stop.isPending}
                            canStop={restarting || selectServer.isPending}
                            onRestart={() => restart.mutate()}
                            onStart={() => start.mutate()}
                            onStop={() => stop.mutate()}
                            onSelfTest={() =>
                                api.post<SelfTestResult>('/api/xkeen/selftest')
                            }
                            onToggleWatchdog={active =>
                                toggleWatchdog.mutate(active)
                            }
                            loading={
                                restart.isPending ||
                                start.isPending ||
                                stop.isPending ||
                                restarting
                            }
                        />
                        {!s?.verified_failover && (
                            <XKeenCard
                                status={s}
                                pool={pool.data}
                                onEnablePool={() => poolAction.mutate('enable')}
                                onDisablePool={() =>
                                    poolAction.mutate('disable')
                                }
                                onSyncPool={() => poolAction.mutate('sync')}
                                onSyncMihomo={() => syncMihomo.mutate()}
                                lastSync={poolAction.data}
                                loading={
                                    poolAction.isPending ||
                                    syncMihomo.isPending ||
                                    restarting
                                }
                            />
                        )}
                        <SettingsCard />
                        <PasskeyCard />
                        <LogViewer
                            logs={logs.data ?? []}
                            onRefresh={() =>
                                qc.invalidateQueries({ queryKey: ['logs'] })
                            }
                            loading={logs.isFetching}
                        />
                    </div>
                    <div>
                        <ServerList
                            checking={checkingLatency}
                            servers={servers.data ?? []}
                            onSelect={id => selectServer.mutate(id)}
                            onSetCountry={(id, country) =>
                                setCountry.mutate({ id, country })
                            }
                            onCheckAll={checkLatency}
                            loading={selectServer.isPending || restarting}
                        />
                    </div>
                </div>
            </main>
        </div>
    )
}
