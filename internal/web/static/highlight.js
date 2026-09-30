'use strict';

// tokenize splits a cell into pieces to colour. Every character of the cell
// is in exactly one piece, in order, so the coloured text lines up with the
// text being typed. It reads a cell the way mote does, loosely: it only
// colours, and the server says whether the cell is any good.
function tokenize(src) {
  const out = [];
  const push = (cls, text) => { if (text) out.push({ cls, text }); };
  const n = src.length;
  let i = 0;
  let stage = true; // the next word is a task: at the start, or after a |

  // name = pipeline
  const binding = /^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*)=/.exec(src);
  if (binding) {
    push('', binding[1]);
    push('name', binding[2]);
    push('', binding[3]);
    push('op', '=');
    i = binding[0].length;
  }

  // The end of a quoted piece that starts at i: past its closing quote, or the
  // end of the text if it is never closed.
  const endOfQuote = (from) => {
    const q = src[from];
    let j = from + 1;
    while (j < n && src[j] !== q) j += src[j] === '\\' ? 2 : 1;
    return Math.min(j + 1, n);
  };

  const pushString = (text) => {
    for (const part of text.split(/(\{\{.*?\}\})/)) push(/^\{\{.*\}\}$/.test(part) ? 'ref' : 'str', part);
  };

  while (i < n) {
    const c = src[i];
    if (/\s/.test(c)) {
      let j = i;
      while (j < n && /\s/.test(src[j])) j++;
      push('', src.slice(i, j));
      i = j;
    } else if (c === '|') {
      push('op', '|');
      i++;
      stage = true;
    } else if (c === '"' || c === "'") {
      const j = endOfQuote(i);
      pushString(src.slice(i, j));
      i = j;
      stage = false;
    } else if (src.startsWith('{{', i)) {
      const k = src.indexOf('}}', i + 2);
      const j = k < 0 ? n : k + 2;
      push('ref', src.slice(i, j));
      i = j;
      stage = false;
    } else if (stage && (src.startsWith('sh:', i) || c === '!')) {
      // A shell command runs to the next | that is not inside quotes.
      const mark = c === '!' ? 1 : 3;
      push('sh', src.slice(i, i + mark));
      let j = i + mark;
      const from = j;
      while (j < n && src[j] !== '|') j = src[j] === '"' || src[j] === "'" ? endOfQuote(j) : j + 1;
      push('shell', src.slice(from, j));
      i = j;
      stage = false;
    } else {
      let j = i;
      while (j < n && !/[\s|"']/.test(src[j]) && !src.startsWith('{{', j)) j++;
      const word = src.slice(i, j);
      if (stage) push('task', word);
      else if (word === '{}' || word === '-') push('ph', word);
      else if (/^\d+(\.\d+)?$/.test(word)) push('num', word);
      else push('', word);
      i = j;
      stage = false;
    }
  }
  return out;
}

if (typeof module !== 'undefined') module.exports = { tokenize };
