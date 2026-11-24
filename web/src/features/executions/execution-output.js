import { appendTerminalOutput } from './terminal-output.js';

export function createOutputBuffer() {
  return {
    text: '',
    cursor: 0,
    after: 0,
    decoder: new globalThis.TextDecoder(),
    escape: '',
    loaded: false,
    chunks: [],
    bytes: 0,
    hasEarlier: false,
    history: null,
  };
}

// Start at the visible tail, then continue by cursor. Drain a bounded number of
// pages immediately; a busy terminal can continue on the next immediate poll.
export async function readTerminalOutput(buffer, path, request, signal, onUpdate) {
  if (buffer.complete) {
    return false;
  }
  for (let page = 0; page < 4; page++) {
    const tail = !buffer.loaded;
    const result = await request(`${path}?${tail ? 'tail=1' : `after=${buffer.after}`}`, {
      signal,
    });
    if (signal.aborted) {
      return false;
    }
    buffer.loaded = true;
    buffer.trimmed ||= !!result.truncated;
    buffer.hasEarlier ||= !!result.truncated;
    appendOutputChunks(buffer, result.chunks);
    await onUpdate();
    if (signal.aborted || tail || result.chunks.length < 16) {
      return false;
    }
  }
  return true;
}

function decodeChunk(chunk) {
  return {
    id: chunk.id,
    offset: chunk.offset || 0,
    bytes: Uint8Array.from(globalThis.atob(chunk.data), (character) => character.charCodeAt(0)),
  };
}

// Keep a bounded tail for quick return to latest. Once the user scrolls away,
// retain the displayed history and append live bytes there as well.
export function appendOutputChunks(buffer, chunks) {
  for (const chunk of chunks) {
    if (chunk.id <= buffer.after) {
      continue;
    }
    const decoded = decodeChunk(chunk);
    appendTerminalOutput(buffer, buffer.decoder.decode(decoded.bytes, { stream: true }));
    if (buffer.history) {
      appendTerminalOutput(
        buffer.history,
        buffer.history.decoder.decode(decoded.bytes, { stream: true }),
        Infinity,
      );
      buffer.history.chunks.push(decoded);
    }
    buffer.chunks.push(decoded);
    buffer.bytes += decoded.bytes.length;
    buffer.after = chunk.id;
  }
  while (buffer.bytes > 262144 && buffer.chunks.length) {
    const oldest = buffer.chunks[0];
    const remove = Math.min(oldest.bytes.length, buffer.bytes - 262144);
    buffer.bytes -= remove;
    if (remove === oldest.bytes.length) {
      buffer.chunks.shift();
    } else {
      buffer.chunks[0] = {
        ...oldest,
        offset: oldest.offset + remove,
        bytes: oldest.bytes.slice(remove),
      };
    }
    buffer.hasEarlier = true;
  }
}

function renderChunks(chunks) {
  const state = { text: '', cursor: 0, escape: '', decoder: new globalThis.TextDecoder() };
  for (const chunk of chunks) {
    appendTerminalOutput(state, state.decoder.decode(chunk.bytes, { stream: true }), Infinity);
  }
  return state;
}

export function expandOutputHistory(buffer) {
  if (!buffer.history) {
    buffer.history = {
      chunks: [...buffer.chunks],
      hasEarlier: buffer.hasEarlier,
      ...renderChunks(buffer.chunks),
    };
  }
  return buffer.history;
}

export function historyCursor(buffer) {
  const first = (buffer.history?.chunks || buffer.chunks)[0];
  return first ? { before: first.id, offset: first.offset } : null;
}

export function prependOutputPage(buffer, page) {
  const history = expandOutputHistory(buffer);
  const older = page.chunks.map(decodeChunk);
  const current = history.chunks;
  if (older.length && current.length && older.at(-1).id === current[0].id) {
    // A page boundary may split a single database chunk. Join its byte slices
    // before decoding, so split UTF-8 and terminal escapes remain intact.
    const prefix = older.pop();
    const suffix = current[0];
    if (prefix.offset + prefix.bytes.length !== suffix.offset) {
      throw new Error('History page does not match its cursor. Scroll up to retry.');
    }
    const joined = new Uint8Array(prefix.bytes.length + suffix.bytes.length);
    joined.set(prefix.bytes);
    joined.set(suffix.bytes, prefix.bytes.length);
    older.push({ id: prefix.id, offset: prefix.offset, bytes: joined });
    history.chunks = [...older, ...current.slice(1)];
  } else {
    if (older.length && current.length && older.at(-1).id >= current[0].id) {
      throw new Error('History page overlaps the displayed output. Scroll up to retry.');
    }
    history.chunks = [...older, ...current];
  }
  history.hasEarlier = !!page.has_more;
  Object.assign(history, renderChunks(history.chunks));
  return history.text;
}
