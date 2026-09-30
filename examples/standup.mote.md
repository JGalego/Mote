# Stand-up notes

<audio controls src="meeting.m4a"></audio>

Ten seconds of a stand-up, turned into follow-ups. Run it again with `mote nb run examples/standup.mote.md`: a cell whose inputs have not changed is not computed again.

## What was said

```mote as=talk
transcribe examples/meeting.m4a
```

```output key=c4f6ea66e0d3cf7b
In today's stand-up, we decided to ship the notebook console on Friday. Anna will write the release notes, and Ben will record the demo.
```

## Decisions and action items

What the transcript comes to, as a list.

```mote as=todo
summarize "{{talk}}" "decisions and action items"
```

```output key=c55f859d0d695edd
*   **Decision:** Ship the notebook console on Friday.
*   **Action Item:** Anna will write release notes.
*   **Action Item:** Ben will record the demo.
*   **Action Item:** The notebook console is scheduled for shipping on Friday.
```

## The same in French

For anyone who would rather read it in French.

```mote
translate French "{{todo}}"
```

```output key=fd10d82d13dd0138
*   **Décision :** L'ordinateur de console sera livré le vendredi.
*   **Action :** Anna rédigera les notes de sortie.
*   **Action :** Ben enregistrera le démonstration.
*   **Action :** L'ordinateur de console est prévu pour être livré le vendredi.
```

## For the team channel

And a line to post.

```mote
chat "write a one-line Slack message from: {{todo}}"
```

```output key=2a9f78a6c60032dc
We are shipping the notebook console on Friday, with Anna writing release notes and Ben recording the demo!
```
