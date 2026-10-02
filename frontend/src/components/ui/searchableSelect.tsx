import { useState } from 'react'
import { Combobox } from '@base-ui/react/combobox'
import { IconCheck, IconChevronDown, IconSearch } from '@tabler/icons-react'

type Option = { value: string; label: string }

const normalize = (value: string) =>
    value
        .trim()
        .toLocaleLowerCase('ru')
        .normalize('NFKD')
        .replace(/\p{M}/gu, '')
        .replace(/ё/g, 'е')

export function SearchableSelect({
    id,
    label,
    placeholder,
    options,
    value,
    onChange,
}: {
    id: string
    label: string
    placeholder: string
    options: Option[]
    value: string
    onChange: (value: string) => void
}) {
    const [query, setQuery] = useState('')
    const selected = options.find(option => option.value === value) ?? null
    return (
        <Combobox.Root
            items={options}
            value={selected}
            inputValue={query}
            onInputValueChange={setQuery}
            onOpenChange={open => {
                if (!open) setQuery('')
            }}
            onValueChange={option => onChange(option?.value ?? '')}
            isItemEqualToValue={(a, b) => a.value === b.value}
            filter={(option: Option, text: string) => {
                const searchable = normalize(`${option.label} ${option.value}`)
                return normalize(text)
                    .split(/\s+/)
                    .every(word => searchable.includes(word))
            }}
            autoHighlight
            autoComplete='off'
        >
            <Combobox.Trigger
                id={id}
                aria-label={label}
                className='flex w-full min-w-0 items-center justify-between gap-2 rounded-md border bg-background px-3 py-2 text-left text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
            >
                <span className='truncate' title={selected?.label}>
                    <Combobox.Value placeholder={placeholder} />
                </span>
                <IconChevronDown className='size-4 shrink-0 text-muted-foreground' />
            </Combobox.Trigger>
            <Combobox.Portal>
                <Combobox.Positioner
                    sideOffset={6}
                    align='start'
                    className='z-50'
                >
                    <Combobox.Popup className='w-[var(--anchor-width)] min-w-[min(360px,var(--available-width))] max-w-[var(--available-width)] max-h-[var(--available-height)] overflow-hidden rounded-lg border bg-card shadow-lg'>
                        <div className='flex items-center gap-2 border-b px-3 py-2'>
                            <IconSearch className='size-4 shrink-0 text-muted-foreground' />
                            <Combobox.Input
                                aria-label={`Поиск: ${label}`}
                                placeholder='Введите часть названия…'
                                className='w-full min-w-0 bg-transparent py-1 text-sm outline-none placeholder:text-muted-foreground'
                            />
                        </div>
                        <Combobox.Empty
                            className='px-3 py-4 text-sm text-muted-foreground empty:p-0'
                            role='status'
                        >
                            Ничего не найдено
                        </Combobox.Empty>
                        <Combobox.List className='max-h-64 overflow-y-auto overscroll-contain p-1 empty:p-0'>
                            {(option: Option) => (
                                <Combobox.Item
                                    key={option.value}
                                    value={option}
                                    className='flex cursor-pointer items-start gap-2 rounded px-2 py-2 text-sm outline-none data-highlighted:bg-accent data-highlighted:text-accent-foreground'
                                >
                                    <span className='min-w-0 flex-1 break-words'>
                                        {option.label}
                                    </span>
                                    <Combobox.ItemIndicator>
                                        <IconCheck className='mt-0.5 size-4 shrink-0' />
                                    </Combobox.ItemIndicator>
                                </Combobox.Item>
                            )}
                        </Combobox.List>
                    </Combobox.Popup>
                </Combobox.Positioner>
            </Combobox.Portal>
        </Combobox.Root>
    )
}
