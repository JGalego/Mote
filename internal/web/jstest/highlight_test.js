'use strict';
const assert = require('node:assert');
const { tokenize } = require('../static/highlight.js');

const classes = (src) => tokenize(src).filter((t) => t.cls).map((t) => t.cls + ':' + t.text);
const rejoin = (src) => tokenize(src).map((t) => t.text).join('');

// a task, a string with a reference in it, and a number
assert.deepStrictEqual(classes('chat "what is {{city}}?" 3'), ['task:chat', 'str:"what is ', 'ref:{{city}}', 'str:?"', 'num:3']);
// a pipeline: each stage starts with a task; {} and - are the piped value
assert.deepStrictEqual(classes('frames clip.mp4 3 | describe - "what?"'), ['task:frames', 'num:3', 'op:|', 'task:describe', 'ph:-', 'str:"what?"']);
assert.deepStrictEqual(classes("code 'x' | chat {}"), ['task:code', 'str:\'x\'', 'op:|', 'task:chat', 'ph:{}']);
// a binding
assert.deepStrictEqual(classes('city = chat "hi"'), ['name:city', 'op:=', 'task:chat', 'str:"hi"']);
assert.deepStrictEqual(classes('city=chat hi'), ['name:city', 'op:=', 'task:chat']);
// a shell stage runs to the next | outside quotes, and is not read as tasks
assert.deepStrictEqual(classes('chat hi | sh: grep -i "a|b" | wc -l | chat -'), [
  'task:chat', 'op:|', 'sh:sh:', 'shell: grep -i "a|b" ', 'op:|', 'task:wc', 'op:|', 'task:chat', 'ph:-',
]);
assert.deepStrictEqual(classes('!echo hi'), ['sh:!', 'shell:echo hi']);
// a reference on its own, and an unclosed one
assert.deepStrictEqual(classes('summarize {{talk}} "focus"'), ['task:summarize', 'ref:{{talk}}', 'str:"focus"']);
assert.deepStrictEqual(classes('chat {{oops'), ['task:chat', 'ref:{{oops']);
// a quote that is never closed runs to the end
assert.deepStrictEqual(classes('chat "unclosed'), ['task:chat', 'str:"unclosed']);
assert.deepStrictEqual(classes('chat "a \\" b" x'), ['task:chat', 'str:"a \\" b"']);
// an equals sign that is not a binding
assert.deepStrictEqual(classes('chat a=b'), ['task:chat']);
// nothing to colour
assert.deepStrictEqual(tokenize(''), []);
assert.deepStrictEqual(classes('   \n  '), []);

// Every character is in exactly one piece, in order, whatever the text.
const samples = ['', ' ', '\n', 'chat', 'a = ', '=', '==', '| |', '"', "'", '{{', '}}', '{', '\\', '\\"', '!', 'sh:', 'x |', '| x',
  'chat "a" | sh: x "b|c" | d', 'x = y = z', 'é "ü" {{ñ}}', '\t\ttabs\tand\n\nlines\n', 'a\r\nb'];
for (const s of samples) assert.strictEqual(rejoin(s), s, JSON.stringify(s));
let seed = 12345;
const rnd = (n) => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) % n;
const alphabet = ['a', 'b', 'c', ' ', '\n', '\t', '|', '"', "'", '{', '}', '=', '-', '!', ':', 's', 'h', '\\', '1', '.', 'é', '{{', '}}', 'sh:'];
for (let k = 0; k < 5000; k++) {
  let s = '';
  for (let j = rnd(30); j > 0; j--) s += alphabet[rnd(alphabet.length)];
  assert.strictEqual(rejoin(s), s, JSON.stringify(s));
  for (const t of tokenize(s)) assert.ok(t.text.length > 0, 'empty piece in ' + JSON.stringify(s));
}
console.log('highlight: ok');
