// Keep listing counts and picker selection consistent as tag membership changes.
export function resolveJobHostIDs(
  { host_ids = [], host_tags = [], excluded_host_ids = [] },
  hosts,
) {
  const selected = new Set(host_ids);
  const tags = new Set(host_tags);
  for (const host of hosts) {
    if (host.tags.some((tag) => tags.has(tag))) {
      selected.add(host.id);
    }
  }
  for (const id of excluded_host_ids) {
    selected.delete(id);
  }
  return selected;
}
