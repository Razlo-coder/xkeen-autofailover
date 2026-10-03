import { useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { IconRefresh } from '@tabler/icons-react'
import type { SubscriptionInfo } from '@/types'

type Source = {
    id: number
    url: string
    last_updated: string
    server_count: number
}

function formatDate(dateStr: string) {
    if (!dateStr || dateStr.startsWith('0001-')) return '—'
    return new Date(dateStr).toLocaleString('ru-RU', {
        day: '2-digit',
        month: '2-digit',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
    })
}

function SourceForm({
    source,
    onUpdate,
    loading,
}: {
    source: Source
    onUpdate: (source: number, url: string) => void
    loading: boolean
}) {
    const [url, setUrl] = useState('')
    const [expanded, setExpanded] = useState(false)
    const submit = (event: React.FormEvent) => {
        event.preventDefault()
        if (!url.trim()) return
        onUpdate(source.id, url.trim())
        setUrl('')
    }
    return (
        <div className='space-y-2 border-t pt-3 first:border-t-0 first:pt-0'>
            <div className='flex items-center justify-between text-sm font-medium'>
                <span>Подписка {source.id + 1}</span>
                <span className='text-muted-foreground font-normal'>
                    {source.server_count} серверов
                </span>
            </div>
            {source.url ? (
                <div className='space-y-1 text-xs'>
                    <button
                        type='button'
                        onClick={() => setExpanded(!expanded)}
                        className='text-primary hover:underline'
                    >
                        {expanded ? 'Скрыть URL' : 'Показать URL'}
                    </button>
                    <p
                        className={
                            expanded
                                ? 'break-all rounded-md bg-muted p-2 font-mono'
                                : 'truncate text-muted-foreground'
                        }
                    >
                        {expanded
                            ? source.url
                            : `${source.url.substring(0, 34)}…`}
                    </p>
                    <p className='text-muted-foreground'>
                        Обновлено: {formatDate(source.last_updated)}
                    </p>
                </div>
            ) : (
                <p className='text-xs text-muted-foreground'>Не добавлена</p>
            )}
            <form onSubmit={submit} className='flex gap-2'>
                <Input
                    type='url'
                    value={url}
                    onChange={event => setUrl(event.target.value)}
                    placeholder={
                        source.id === 0
                            ? 'URL основной подписки'
                            : 'URL второй подписки'
                    }
                />
                <Button
                    type='submit'
                    variant='outline'
                    disabled={!url.trim() || loading}
                >
                    {source.url ? 'Заменить' : 'Добавить'}
                </Button>
            </form>
            {source.id === 1 && source.url && (
                <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    disabled={loading}
                    onClick={() => onUpdate(1, '')}
                    className='text-muted-foreground'
                >
                    Удалить вторую подписку
                </Button>
            )}
        </div>
    )
}

export function SubscriptionForm({
    subscription,
    onUpdate,
    onRefresh,
    loading,
}: {
    subscription: SubscriptionInfo | null
    onUpdate: (source: number, url: string) => void
    onRefresh: () => void
    loading: boolean
}) {
    const sources = subscription?.sources ?? [
        {
            id: 0,
            url: subscription?.url ?? '',
            last_updated: subscription?.last_updated ?? '',
            server_count: subscription?.server_count ?? 0,
        },
        { id: 1, url: '', last_updated: '', server_count: 0 },
    ]
    return (
        <Card>
            <CardHeader className='flex flex-row items-center justify-between pb-3'>
                <CardTitle className='text-base'>Подписки</CardTitle>
                <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={onRefresh}
                    disabled={loading || !sources.some(source => source.url)}
                    title='Обновить обе подписки и остановить текущий пинг'
                >
                    <IconRefresh className={loading ? 'animate-spin' : ''} />
                    Обновить
                </Button>
            </CardHeader>
            <CardContent className='space-y-4'>
                {sources.map(source => (
                    <SourceForm
                        key={source.id}
                        source={source}
                        onUpdate={onUpdate}
                        loading={loading}
                    />
                ))}
                <p className='text-xs text-muted-foreground'>
                    Серверы обеих подписок участвуют в проверке и переключении.
                    Их порядок задаётся в правилах ниже.
                </p>
            </CardContent>
        </Card>
    )
}
