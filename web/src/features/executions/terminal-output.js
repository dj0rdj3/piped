// Render terminal text without interpreting HTML, hyperlinks, or control
// sequences. State is preserved across network chunks, including split escapes.
export function appendTerminalOutput(state, text, maxLength = 262144) {
  // CRLF is a line break, including when the terminal produces very short
  // lines at high speed. Append ordinary multiline runs in one operation;
  // scanning/rebuilding the accumulated log per character blocks browser input.
  text = text.replace(/\r\n/g, '\n');
  const special = /[^\P{Cc}\n\t]/gu;
  const controls = /\p{Cc}/gu;
  let index = 0;
  while (index < text.length) {
    if (!state.escape && state.cursor === state.text.length) {
      special.lastIndex = index;
      const nextSpecial = special.exec(text);
      const end = nextSpecial ? nextSpecial.index : text.length;
      if (end > index) {
        state.text += text.slice(index, end);
        state.cursor = state.text.length;
        index = end;
        continue;
      }
    }
    const character = text[index];
    if (state.escape) {
      index++;
      state.escape += character;
      if (state.escape.startsWith('\u001b]')) {
        if (character === '\u0007' || state.escape.endsWith('\u001b\\')) {
          state.escape = '';
        }
      } else if (state.escape.length > 2 && /[@-~]/.test(character)) {
        state.escape = '';
      } else if (state.escape.length === 2 && character !== '[') {
        state.escape = '';
      }
      if (state.escape.length > 4096) {
        state.escape = '';
      }
      continue;
    }
    if (character === '\u001b') {
      state.escape = character;
    } else if (character === '\r') {
      state.cursor = state.text.lastIndexOf('\n') + 1;
    } else if (character === '\b') {
      state.cursor = Math.max(state.text.lastIndexOf('\n') + 1, state.cursor - 1);
    } else if (character === '\n') {
      state.text += '\n';
      state.cursor = state.text.length;
    } else if (character === '\t' || !/\p{Cc}/u.test(character)) {
      // Append printable runs at once. This keeps rebuilding a growing history
      // page linear for ordinary logs instead of copying on every character.
      controls.lastIndex = index + 1;
      const nextControl = controls.exec(text);
      const end = nextControl ? nextControl.index : text.length;
      const run = text.slice(index, end);
      if (state.cursor === state.text.length) {
        state.text += run;
      } else {
        state.text =
          state.text.slice(0, state.cursor) + run + state.text.slice(state.cursor + run.length);
      }
      state.cursor += run.length;
      index = end;
      continue;
    }
    index++;
  }
  if (state.text.length > maxLength) {
    const removed = state.text.length - maxLength;
    state.text = state.text.slice(removed);
    state.cursor = Math.max(0, state.cursor - removed);
    state.trimmed = true;
  }
}
