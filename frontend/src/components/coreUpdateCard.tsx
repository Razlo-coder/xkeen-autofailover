import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

type Release = { tag: string; url: string; published_at: string }
type CoreUpdateState = {
    current: string
    latest?: Release
    available: boolean
    supported: boolean
    reason?: string
    previous?: string
    updating: boolean
    step?: string
    check_error?: string
    apply_error?: string
}

export function CoreUpdateCard() {
    const qc = useQueryClient()
    const state = useQuery({
        queryKey: ['core-update'],
        queryFn: () => api.get<CoreUpdateState>('/api/core-update'),
        refetchInterval: query => query.state.data?.updating ? 2000 : false,
    })
    const action = useMutation({
        mutationFn: (name: 'check' | 'apply' | 'rollback') =>
            api.post<CoreUpdateState>(`/api/core-update/${name}`),
        onSuccess: data => qc.setQueryData(['core-update'], data),
    })
    const s = state.data

    return (
        <Card>
            <CardHeader className='pb-3'>
                <CardTitle className='text-base'>Ядро Xray</CardTitle>
            </CardHeader>
            <CardContent className='space-y-3 text-sm'>
                {state.isLoading && <p className='text-muted-foreground'>Загружаю сведения…</p>}
                {state.error && <p className='text-destructive'>{state.error.message}</p>}
                {s && <>
                    <p>Установлено: <strong>{s.current || 'неизвестно'}</strong></p>
                    {s.latest && <p>Доступно: <a className='underline' href={s.latest.url} target='_blank' rel='noreferrer'>{s.latest.tag}</a></p>}
                    {!s.supported && <p className='text-muted-foreground'>{s.reason}</p>}
                    {s.updating && <p>{s.step || 'Обновляю ядро…'}</p>}
                    {s.check_error && <p className='text-destructive'>Проверка версии: {s.check_error}</p>}
                    {s.apply_error && <p className='text-destructive'>Обновление: {s.apply_error}</p>}
                    <div className='flex flex-wrap gap-2'>
                        <Button variant='outline' size='sm' disabled={!s.supported || s.updating || action.isPending} onClick={() => action.mutate('check')}>
                            Проверить обновления
                        </Button>
                        {s.available && <Button size='sm' disabled={s.updating || action.isPending} onClick={() => action.mutate('apply')}>
                            Обновить Xray
                        </Button>}
                        {s.previous && <Button variant='outline' size='sm' disabled={s.updating || action.isPending} onClick={() => action.mutate('rollback')}>
                            Вернуть {s.previous}
                        </Button>}
                    </div>
                    {s.available && <p className='text-muted-foreground'>При установке Xray кратко перезапустится. Конфигурация проверяется заранее; при ошибке запуска прежнее ядро будет возвращено.</p>}
                </>}
                {action.error && <p className='text-destructive'>{action.error.message}</p>}
            </CardContent>
        </Card>
    )
}
