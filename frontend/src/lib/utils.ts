export function formatDate(d: string | Date | null | undefined, opts?: Intl.DateTimeFormatOptions): string {
  if (!d) return '—';
  const date = typeof d === 'string' ? new Date(d) : d;
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString(undefined, opts ?? { dateStyle: 'medium', timeStyle: 'short' });
}

export function splitTrimmedLines(text: string): string[] {
  return text.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
}

export function relativeTime(d: string | Date | null | undefined): string {
  if (!d) return '—';
  const date = typeof d === 'string' ? new Date(d) : d;
  const diff = (date.getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  const fmt = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
  if (abs < 60) return fmt.format(Math.round(diff), 'second');
  if (abs < 3600) return fmt.format(Math.round(diff / 60), 'minute');
  if (abs < 86400) return fmt.format(Math.round(diff / 3600), 'hour');
  return fmt.format(Math.round(diff / 86400), 'day');
}
