# Demo recordings

The GIFs are real runs recorded with [vhs](https://github.com/charmbracelet/vhs)
against a throwaway `HOME=/tmp/mote-demo`, on a 4-core x86_64 CPU without GPU.

```sh
mkdir -p /tmp/mote-demo
vhs docs/demo/install.tape                     # installs and sets up mote
HOME=/tmp/mote-demo PATH=/tmp/mote-demo/.local/bin:$PATH mote models pull asr tts
HOME=/tmp/mote-demo PATH=/tmp/mote-demo/.local/bin:$PATH sh docs/demo/prep.sh
for t in chat code refactor doc extract describe transcribe speak frames video convert patch; do
  vhs docs/demo/$t.tape
done
sh docs/demo/crop.sh docs/demo/*.gif          # trim unused space below the text
```
