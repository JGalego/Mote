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
for t in chat code refactor doc extract describe transcribe speak frames video convert patch \
         pipe plan agent; do
  vhs docs/demo/$t.tape
done
sh docs/demo/crop.sh docs/demo/*.gif          # trim unused space below the text
```

`plan` and `agent` use `--profile balanced`, as the README recommends for
them; the 0.8B model of the default profile is too small to plan or act
reliably. The agent's shell runs sandboxed when bubblewrap is installed.
