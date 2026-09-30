# Demo recordings

The GIFs are real runs recorded with [vhs](https://github.com/charmbracelet/vhs)
against a throwaway `HOME=/tmp/mote-demo`, on a 4-core x86_64 CPU without GPU.

```sh
mkdir -p /tmp/mote-demo
vhs docs/demo/install.tape                     # installs and sets up mote
# XDG variables take precedence over HOME, so they are pinned as well.
demo="env HOME=/tmp/mote-demo XDG_DATA_HOME=/tmp/mote-demo/.local/share XDG_CONFIG_HOME=/tmp/mote-demo/.config PATH=/tmp/mote-demo/.local/bin:$PATH"
$demo mote models pull asr tts
$demo mote models pull qwen3.5-2b
$demo sh docs/demo/prep.sh
cp examples/meeting.m4a examples/clip.mp4 /tmp/mote-demo/demo/
for t in chat code refactor doc extract describe transcribe speak frames video convert patch \
         pipe plan agent nb-run nb-console; do
  vhs docs/demo/$t.tape
done
sh docs/demo/crop.sh docs/demo/*.gif          # trim unused space below the text
```

`plan` and `agent` use `--profile balanced`, as the README recommends for
them; the 0.8B model of the default profile is too small to plan or act
reliably. The agent's shell runs sandboxed when bubblewrap is installed.

`nb-run` expects `~/demo/standup.src.md`, and `nb-serve.gif` is recorded in a
browser rather than a terminal: [`nb-serve.mjs`](nb-serve.mjs) drives a
headless Chromium through the DevTools protocol while `mote nb serve` runs,
and holds a frame that does not change for at most 1.5 s, so the waits on a
model are shortened. With `~/demo/browser.src.md` copied to
`browser.mote.md`:

```sh
cd /tmp/mote-demo/demo && $demo mote nb serve browser.mote.md --port 8791 &
node --experimental-websocket docs/demo/nb-serve.mjs "http://127.0.0.1:8791/?token=..."
```

The two notebooks:

````markdown
# Stand-up

```mote as=talk
transcribe meeting.m4a
```

```mote
chat "List the decisions in two short bullets: {{talk}}"
```
````

````markdown
# Stand-up, in the browser

Ten seconds of a stand-up, and a clip to look at.

<audio controls src="meeting.m4a"></audio>

```mote as=talk
transcribe meeting.m4a
```

```mote
chat "Who writes the release notes? Name only. {{talk}}"
```

```mote
frames clip.mp4 2
```
````
