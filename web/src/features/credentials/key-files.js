// A cancelled picker is a no-op: keep any key the user already pasted.
export async function readKeyFile(input, limit) {
  const file = input.files?.[0];
  input.value = '';
  if (!file) {
    return null;
  }
  if (file.size > limit) {
    throw new Error(`Key file must be at most ${limit / 1024} KiB.`);
  }
  try {
    return await file.text();
  } catch {
    throw new Error('Could not read the key file.');
  }
}
