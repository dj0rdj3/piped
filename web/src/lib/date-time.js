const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function formatDateTime({ year, month, day, hour, minute }, currentYear) {
  const pad = (value) => String(value).padStart(2, '0');
  const yearLabel = Number(year) === currentYear ? '' : ` ${year}`;
  return `${pad(day)} ${months[Number(month) - 1]}${yearLabel}, ${pad(hour)}:${pad(minute)}`;
}

// Execution and account timestamps use the client's local calendar and clock.
export function formatTimestamp(unixSeconds, currentYear = new Date().getFullYear()) {
  const date = new Date(unixSeconds * 1000);
  return formatDateTime(
    {
      year: date.getFullYear(),
      month: date.getMonth() + 1,
      day: date.getDate(),
      hour: date.getHours(),
      minute: date.getMinutes(),
    },
    currentYear,
  );
}

// Preserve the server's wall clock; its timezone is shown above the preview.
// Year omission still follows the client's current year, not the server's.
export function formatScheduleTime(timestamp, currentYear = new Date().getFullYear()) {
  const parts = timestamp.match(
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):\d{2}(?:Z|[+-]\d{2}:\d{2})$/,
  );
  if (!parts) {
    return timestamp;
  }
  const [, year, month, day, hour, minute] = parts;
  return formatDateTime({ year, month, day, hour, minute }, currentYear);
}
