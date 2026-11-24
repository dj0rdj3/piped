// Index every 256th display row rather than allocating a string or DOM node for
// every log line. Append updates resume at the final row, including split lines.
export function createTerminalLayout() {
  let text = '';
  let columns = 0;
  let count = 0;
  let lastStart = 0;
  let measure = () => 1;
  const checkpoints = [];
  const segmenter = new Intl.Segmenter(undefined, { granularity: 'grapheme' });

  function scan(start, visit) {
    let position = start;
    while (position <= text.length) {
      const newline = text.indexOf('\n', position);
      const end = newline < 0 ? text.length : newline;
      const line = text.slice(position, end);
      if (/^[\x20-\x7e]*$/.test(line)) {
        if (position === end && visit(position, end) === false) {
          return;
        }
        for (let cursor = position; cursor < end; cursor += columns) {
          if (visit(cursor, Math.min(end, cursor + columns)) === false) {
            return;
          }
        }
      } else {
        let rowStart = position;
        let width = 0;
        for (const { segment, index } of segmenter.segment(line)) {
          const size = segment === '\t' ? 8 - (Math.round(width) % 8) : measure(segment);
          if (width > 0 && width + size > columns) {
            if (visit(rowStart, position + index) === false) {
              return;
            }
            rowStart = position + index;
            width = 0;
          }
          width += segment === '\t' ? 8 - (Math.round(width) % 8) : size;
        }
        if (visit(rowStart, end) === false) {
          return;
        }
      }
      if (newline < 0) {
        break;
      }
      position = newline + 1;
    }
  }

  return {
    get count() {
      return count;
    },
    update(value, width, measureCharacter = () => 1) {
      width = Math.max(1, Math.floor(width));
      const append = columns === width && value.startsWith(text) && count > 0;
      const start = append ? lastStart : 0;
      count = append ? count - 1 : 0;
      checkpoints.length = Math.ceil(count / 256);
      text = value;
      columns = width;
      measure = measureCharacter;
      scan(start, (rowStart) => {
        if (count % 256 === 0) {
          checkpoints.push(rowStart);
        }
        lastStart = rowStart;
        count++;
      });
    },
    window(first, size) {
      first = Math.max(0, Math.min(count - 1, Math.floor(first)));
      const checkpoint = Math.floor(first / 256);
      let row = checkpoint * 256;
      const visible = [];
      scan(checkpoints[checkpoint] || 0, (start, end) => {
        if (row++ >= first) {
          visible.push(text.slice(start, end));
        }
        return visible.length < size;
      });
      return visible.join('\n');
    },
  };
}
