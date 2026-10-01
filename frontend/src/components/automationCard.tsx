import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import type { AutomationSettings, Server } from '@/types'

const regionNames = new Intl.DisplayNames(['ru'], { type: 'region' })
export const countryName = (code: string) => regionNames.of(code) || code
const commonCountries =
    'AE AL AM AR AT AU AZ BA BE BG BR BY CA CH CL CN CY CZ DE DK EE ES FI FR GB GE GR HK HR HU ID IE IL IN IS IT JP KR KZ LT LU LV MD ME MK MX MY NL NO NZ PL PT RO RS RU SE SG SI SK TH TR TW UA US UZ VN ZA'.split(
        ' ',
    )
const fieldClass = 'w-full rounded-md border bg-background px-3 py-2 text-sm'
type OrderedKey = 'country_priority' | 'preferred_server_names'

export function AutomationCard({ servers }: { servers: Server[] }) {
    const qc = useQueryClient()
    const settings = useQuery({
        queryKey: ['automation'],
        queryFn: () => api.get<AutomationSettings>('/api/automation'),
    })
    const [draft, setDraft] = useState<AutomationSettings | null>(null)
    const [dirty, setDirty] = useState(false)
    const [country, setCountry] = useState('')
    const [preferred, setPreferred] = useState('')
    useEffect(() => {
        if (settings.data && !dirty) setDraft(settings.data)
    }, [settings.data, dirty])
    const save = useMutation({
        mutationFn: (value: AutomationSettings) =>
            api.put<AutomationSettings>('/api/automation', value),
        onSuccess: value => {
            qc.setQueryData(['automation'], value)
            setDraft(value)
            setDirty(false)
            qc.invalidateQueries({ queryKey: ['status'] })
            qc.invalidateQueries({ queryKey: ['servers'] })
        },
    })
    const change = (patch: Partial<AutomationSettings>) => {
        setDraft(old => (old ? { ...old, ...patch } : old))
        setDirty(true)
    }
    if (!draft)
        return (
            <Card>
                <CardContent className='py-4 text-sm'>
                    {settings.error
                        ? settings.error.message
                        : 'Загрузка правил автовосстановления…'}
                </CardContent>
            </Card>
        )
    const countries = [
        ...new Set([
            ...commonCountries,
            ...draft.country_priority,
            ...servers
                .map(s => s.country_override || s.country || '')
                .filter(Boolean),
        ]),
    ].sort((a, b) => countryName(a).localeCompare(countryName(b), 'ru'))
    const names = [...new Set(servers.map(s => s.name))].sort((a, b) =>
        a.localeCompare(b, 'ru'),
    )
    const move = (key: OrderedKey, index: number, direction: number) => {
        const values = [...draft[key]]
        const target = index + direction
        if (target < 0 || target >= values.length) return
        ;[values[index], values[target]] = [values[target], values[index]]
        change({ [key]: values })
    }
    const orderedList = (key: OrderedKey, label: (value: string) => string) => (
        <ol className='space-y-1'>
            {draft[key].map((value, index) => (
                <li
                    key={value}
                    className='flex items-center gap-2 rounded border px-2 py-1.5 text-sm'
                >
                    <span className='text-muted-foreground'>{index + 1}.</span>
                    <span className='min-w-0 flex-1 break-words'>
                        {label(value)}
                    </span>
                    <button
                        type='button'
                        aria-label={`Поднять ${label(value)}`}
                        disabled={index === 0}
                        onClick={() => move(key, index, -1)}
                        className='px-1 disabled:opacity-25'
                    >
                        ↑
                    </button>
                    <button
                        type='button'
                        aria-label={`Опустить ${label(value)}`}
                        disabled={index === draft[key].length - 1}
                        onClick={() => move(key, index, 1)}
                        className='px-1 disabled:opacity-25'
                    >
                        ↓
                    </button>
                    <button
                        type='button'
                        aria-label={`Удалить ${label(value)}`}
                        onClick={() =>
                            change({
                                [key]: draft[key].filter(v => v !== value),
                            })
                        }
                        className='px-1 text-muted-foreground'
                    >
                        ×
                    </button>
                </li>
            ))}
        </ol>
    )
    return (
        <Card>
            <CardHeader className='pb-3'>
                <CardTitle className='text-base'>
                    Правила автовосстановления
                </CardTitle>
            </CardHeader>
            <CardContent className='space-y-4'>
                <p className='text-xs text-muted-foreground'>
                    Рабочее соединение сохраняется. При отказе подписка
                    обновляется, затем проверяются серверы по этим правилам.
                </p>
                <div className='flex items-center justify-between gap-2'>
                    <label htmlFor='automation-enabled' className='text-sm'>
                        Включать автоматически
                    </label>
                    <Switch
                        id='automation-enabled'
                        checked={draft.enabled}
                        onCheckedChange={enabled => change({ enabled })}
                    />
                </div>
                <div className='space-y-2'>
                    <label
                        htmlFor='country-choice'
                        className='text-sm font-medium'
                    >
                        Приоритет стран
                    </label>
                    {orderedList('country_priority', countryName)}
                    <div className='flex gap-2'>
                        <select
                            id='country-choice'
                            className={fieldClass}
                            value={country}
                            onChange={e => setCountry(e.target.value)}
                        >
                            <option value=''>Выберите страну</option>
                            {countries
                                .filter(
                                    cc => !draft.country_priority.includes(cc),
                                )
                                .map(cc => (
                                    <option key={cc} value={cc}>
                                        {countryName(cc)}
                                    </option>
                                ))}
                        </select>
                        <Button
                            variant='outline'
                            disabled={!country}
                            onClick={() => {
                                change({
                                    country_priority: [
                                        ...draft.country_priority,
                                        country,
                                    ],
                                })
                                setCountry('')
                            }}
                        >
                            Добавить
                        </Button>
                    </div>
                    <label className='flex items-start gap-2 text-sm'>
                        <input
                            type='checkbox'
                            className='mt-1'
                            checked={draft.allow_other_countries}
                            onChange={e =>
                                change({
                                    allow_other_countries: e.target.checked,
                                })
                            }
                        />
                        Проверять остальные страны, если выбранные недоступны
                    </label>
                    <p className='text-xs text-muted-foreground'>
                        Без списка стран и с включённой опцией разрешены все
                        страны, включая нераспознанные.
                    </p>
                </div>
                <div className='space-y-2'>
                    <label
                        htmlFor='preferred-choice'
                        className='text-sm font-medium'
                    >
                        Приоритет серверов
                    </label>
                    <p className='text-xs text-muted-foreground'>
                        Сначала выбирается страна, затем сервер внутри неё.
                        Названия сохраняются при смене IP.
                    </p>
                    {orderedList('preferred_server_names', value => value)}
                    <div className='flex gap-2'>
                        <select
                            id='preferred-choice'
                            className={fieldClass}
                            value={preferred}
                            onChange={e => setPreferred(e.target.value)}
                        >
                            <option value=''>Выберите сервер</option>
                            {names
                                .filter(
                                    name =>
                                        !draft.preferred_server_names.includes(
                                            name,
                                        ),
                                )
                                .map(name => (
                                    <option key={name} value={name}>
                                        {name}
                                    </option>
                                ))}
                        </select>
                        <Button
                            variant='outline'
                            disabled={!preferred}
                            onClick={() => {
                                change({
                                    preferred_server_names: [
                                        ...draft.preferred_server_names,
                                        preferred,
                                    ],
                                })
                                setPreferred('')
                            }}
                        >
                            Добавить
                        </Button>
                    </div>
                </div>
                <details className='space-y-2'>
                    <summary className='cursor-pointer text-sm font-medium'>
                        Исключения
                    </summary>
                    <p className='text-xs text-muted-foreground'>
                        Отмеченные серверы не используются и не проверяются.
                    </p>
                    <div className='max-h-48 overflow-y-auto space-y-1'>
                        {[
                            ...new Set([
                                ...names,
                                ...draft.excluded_server_names,
                            ]),
                        ].map(name => (
                            <label key={name} className='flex gap-2 text-sm'>
                                <input
                                    type='checkbox'
                                    className='mt-1'
                                    checked={draft.excluded_server_names.includes(
                                        name,
                                    )}
                                    onChange={e =>
                                        change({
                                            excluded_server_names: e.target
                                                .checked
                                                ? [
                                                      ...draft.excluded_server_names,
                                                      name,
                                                  ]
                                                : draft.excluded_server_names.filter(
                                                      v => v !== name,
                                                  ),
                                        })
                                    }
                                />
                                <span className='break-words'>{name}</span>
                            </label>
                        ))}
                    </div>
                    <label htmlFor='excluded-phrases' className='block text-sm'>
                        Исключать по фразе в названии
                    </label>
                    <textarea
                        id='excluded-phrases'
                        className={fieldClass}
                        rows={3}
                        value={draft.exclude_name_contains.join('\n')}
                        onChange={e =>
                            change({
                                exclude_name_contains:
                                    e.target.value.split('\n'),
                            })
                        }
                        placeholder='Одна фраза на строку'
                    />
                </details>
                {(save.error || settings.error) && (
                    <p role='alert' className='text-sm text-red-400'>
                        {save.error?.message || settings.error?.message}
                    </p>
                )}
                <Button
                    className='w-full'
                    disabled={!dirty || save.isPending}
                    onClick={() => save.mutate(draft)}
                >
                    {save.isPending
                        ? 'Сохранение…'
                        : dirty
                          ? 'Сохранить правила'
                          : 'Правила сохранены'}
                </Button>
                <p className='text-xs text-muted-foreground'>
                    Настройки сохраняются после перезагрузки роутера. Перезапуск
                    VPN для изменения правил не нужен.
                </p>
            </CardContent>
        </Card>
    )
}
