# Examples

Files to try mote on.

| File | What it is | Try |
| --- | --- | --- |
| `meeting.m4a` | 10 seconds of speech: a stand-up deciding to ship on Friday, with an owner for two follow-ups | `mote run transcribe examples/meeting.m4a` |
| `clip.mp4` | 10 seconds of animation, with no text in it: a sailboat crossing a sea as the sun sets, gulls overhead, narrated by a voice that says what is on screen | `mote run video examples/clip.mp4` or `mote pipe "frames examples/clip.mp4 3 \| describe -"` |
| `photo.jpg` | A drawing of a cafe with a sign | `mote run describe examples/photo.jpg "What is written on the sign?"` or `mote run ocr examples/photo.jpg` |
| `invoice.txt`, `invoice.schema.json` | A small invoice and the fields to pull out of it | `mote run extract examples/invoice.txt examples/invoice.schema.json` |
| `standup.mote.md`, `standup.ipynb` | A notebook that transcribes `meeting.m4a`, lists its decisions, translates them and drafts a message, with the outputs from a real run; the `.ipynb` is the same notebook exported for Jupyter | `mote nb run examples/standup.mote.md` (every cell is unchanged, so nothing is recomputed), or `mote nb export examples/standup.mote.md` |
| `setup.json`, `tools.json` | A setup file for `mote setup --config`, and custom tasks that wrap `rg`, `jq` and `git` | see [Extending mote](../docs/extending.md) |

Cells in the notebooks use paths from the repository root, so run them from there.

They chain, which is what a demo wants:

```sh
mote pipe "transcribe examples/meeting.m4a | summarize - 'decisions and action items'"
```

## Where the media came from

The picture and the animation are drawn by [`scripts/make-demo-media.py`](../scripts/make-demo-media.py), so they are ours. The speech is made by [gTTS](https://github.com/pndurette/gTTS), which sends its sentence to Google Translate's speech service; treat the audio, and the narration of the video, as that service's output. Run the script again to replace any of it.
